package main

import (
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// KnowledgeBase is the read-only SQLite file built by kb/build_kb.py:
// man page sections and package descriptions, an FTS5 index over them,
// and a sqlite-vector embedding column for semantic search.
type KnowledgeBase struct {
	db         *sql.DB
	vectors    bool // sqlite-vector loaded and initialized
	embedder   *Embedder
	prefix     string // nomic task prefix for queries
	dimension  int
	vectorType string // FLOAT16 or FLOAT32, from meta
	candidates int    // quantized-scan candidates to re-rank; 0 = exact scan
	meta       map[string]string
}

// Hit is one search result.
type Hit struct {
	ID                           int64
	Kind, Name, Section, Heading string
	Title, Body                  string
	Score                        float64
	Tree                         string
	Lexical, Semantic, Page      int // ranks, 0 when absent
}

var registerDriver sync.Once

func openKnowledgeBase(s Settings) (*KnowledgeBase, error) {
	if _, err := os.Stat(s.KnowledgeBase); err != nil {
		return nil, fmt.Errorf("knowledge base %s not found; set PUFFY_KB",
			s.KnowledgeBase)
	}
	driver := "sqlite3"
	_, extErr := os.Stat(s.VectorExt)
	if extErr == nil {
		registerDriver.Do(func() {
			sql.Register("sqlite3_vector", &sqlite3.SQLiteDriver{
				Extensions: []string{s.VectorExt},
			})
		})
		driver = "sqlite3_vector"
	}
	db, err := sql.Open(driver, "file:"+s.KnowledgeBase+"?mode=ro&immutable=1")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // vector_init is per connection
	kb := &KnowledgeBase{db: db, meta: map[string]string{}}
	rows, err := db.Query("SELECT key, value FROM meta")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("knowledge base %s: %v", s.KnowledgeBase, err)
	}
	for rows.Next() {
		var key, value string
		rows.Scan(&key, &value)
		kb.meta[key] = value
	}
	rows.Close()
	kb.prefix = kb.meta["query_prefix"]
	fmt.Sscan(kb.meta["dimension"], &kb.dimension)

	kb.vectorType = kb.meta["vector_type"]
	if kb.vectorType == "" {
		kb.vectorType = "FLOAT32" // built before float16 storage
	}
	if kb.meta["quantization"] != "" {
		fmt.Sscan(kb.meta["candidates"], &kb.candidates)
	}
	if driver == "sqlite3_vector" {
		options := fmt.Sprintf("type=%s,dimension=%d,distance=COSINE,"+
			"normalized=1", kb.vectorType, kb.dimension)
		_, err = db.Exec("SELECT vector_init('docs', 'embedding', ?),"+
			" vector_init('pages', 'embedding', ?)", options, options)
		kb.vectors = err == nil
		if kb.vectors && kb.candidates > 0 {
			// Keep the compact index in memory (about 27 MB for TURBO4).
			_, err = db.Exec("SELECT vector_quantize_preload('docs'," +
				" 'embedding'), vector_quantize_preload('pages', 'embedding')")
			if err != nil {
				kb.candidates = 0 // no usable index: exact full scan
			}
		}
	}
	if kb.vectors {
		kb.embedder = newEmbedder(s)
	}
	return kb, nil
}

func (kb *KnowledgeBase) close() {
	if kb.embedder != nil {
		kb.embedder.close()
	}
	kb.db.Close()
}

// Search ranks chunks with three signals and fuses them with reciprocal
// rank fusion (RRF): BM25 over chunk text (exact words, flags, file names),
// vector similarity over chunks (meaning), and vector similarity over page
// cards, the one-line NAME of each page (what the page is for). A chunk
// from a page whose card ranks well is boosted; the best such pages are
// added even when none of their chunks matched. kind is "", "man" or "pkg".
func (kb *KnowledgeBase) Search(query, kind string, limit int) ([]Hit, error) {
	const depth = 40
	const rrf = 60.0
	hits := map[int64]*Hit{}
	get := func(id int64) *Hit {
		if hit, ok := hits[id]; ok {
			return hit
		}
		hit := &Hit{ID: id}
		kb.fill(hit)
		hits[id] = hit
		return hit
	}

	related := map[string]bool{}
	for _, page := range expandQuery(query) {
		related[page] = true
	}
	if match := ftsQuery(query); match != "" {
		ids, err := kb.ids(`SELECT d.id FROM docs_fts f
			JOIN docs d ON d.id = f.rowid
			WHERE docs_fts MATCH ? AND (? = '' OR d.kind = ?)
			ORDER BY bm25(docs_fts, 8.0, 4.0, 2.0, 1.0) LIMIT ?`,
			match, kind, kind, depth)
		if err != nil {
			return nil, err
		}
		for rank, id := range ids {
			hit := get(id)
			hit.Lexical = rank + 1
			hit.Score += 1 / (rrf + float64(rank+1))
		}
	}

	pageRank := map[string]int{}
	if kb.vectors && kb.embedder != nil {
		vector, err := kb.embedder.Embed(kb.prefix + query)
		if err == nil {
			ids, err := kb.nearest("docs", vector, kind, depth)
			if err != nil {
				return nil, err
			}
			for rank, id := range ids {
				hit := get(id)
				hit.Semantic = rank + 1
				hit.Score += 1 / (rrf + float64(rank+1))
			}
			pageIDs, err := kb.nearest("pages", vector, kind, depth)
			if err != nil {
				return nil, err
			}
			rows, err := kb.db.Query(`SELECT rowid, kind, name, section
				FROM pages WHERE rowid IN (`+placeholders(len(pageIDs))+`)`,
				anySlice(pageIDs)...)
			if err != nil {
				return nil, err
			}
			pageKeys := map[int64]string{}
			for rows.Next() {
				var id int64
				var pageKind, name, section string
				rows.Scan(&id, &pageKind, &name, &section)
				pageKeys[id] = pageKind + " " + name + " " + section
			}
			rows.Close()
			for rank, id := range pageIDs {
				pageRank[pageKeys[id]] = rank + 1
			}
		}
	}

	// Pages whose card ranks high but that have no chunk yet: add the
	// chunk that introduces them (DESCRIPTION for man, the package text).
	for key, rank := range pageRank {
		if rank > 8 {
			continue
		}
		parts := strings.SplitN(key, " ", 3)
		var id int64
		err := kb.db.QueryRow(`SELECT id FROM docs WHERE kind = ? AND
			name = ? AND section = ? ORDER BY heading <> 'DESCRIPTION',
			heading = 'NAME', id LIMIT 1`, parts[0], parts[1], parts[2]).
			Scan(&id)
		if err == nil {
			get(id)
		}
	}

	// Pages named by the concept map join as candidates through the chunk
	// that introduces them; they get their boost below.
	for page := range related {
		if kind == "pkg" {
			break
		}
		var id int64
		err := kb.db.QueryRow(`SELECT id FROM docs WHERE kind = 'man' AND
			name = ? ORDER BY heading <> 'DESCRIPTION', id LIMIT 1`, page).
			Scan(&id)
		if err == nil {
			get(id)
		}
	}

	words := map[string]bool{}
	for _, word := range strings.Fields(strings.ToLower(query)) {
		words[strings.Trim(word, "\"'`,.?()")] = true
	}
	list := make([]Hit, 0, len(hits))
	for _, hit := range hits {
		if rank, ok := pageRank[hit.Kind+" "+hit.Name+" "+hit.Section]; ok {
			hit.Page = rank
			hit.Score += 1 / (rrf + float64(rank))
		}
		name := strings.ToLower(hit.Name)
		if words[name] && (len(name) > 4 || strings.ContainsAny(name, "._-")) {
			hit.Score += 1 / rrf // the query names the page: pf.conf, rcctl
		} else if related[name] && hit.Kind == "man" {
			hit.Score += 1.2 / rrf // curated: about one strong signal
		}
		hit.Score *= sourceWeight(hit)
		list = append(list, *hit)
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Score != list[j].Score {
			return list[i].Score > list[j].Score
		}
		return list[i].ID < list[j].ID
	})
	// At most two chunks per page, so one long page cannot fill the list.
	perPage := map[string]int{}
	result := make([]Hit, 0, limit)
	for _, hit := range list {
		key := hit.Kind + hit.Name + hit.Section
		if hit.Kind == "pkg" {
			key = hit.Kind + hit.Name + hit.Heading // one per flavor set
		}
		if perPage[key] == 2 || (hit.Kind == "pkg" && perPage[key] == 1) {
			continue
		}
		perPage[key]++
		result = append(result, hit)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

// sourceWeight prefers what an administrator usually needs: base system
// commands, file formats and drivers over Perl's and X11's library pages.
func sourceWeight(hit *Hit) float64 {
	switch {
	case hit.Kind == "pkg":
		return 0.9
	case hit.Section == "3p" || strings.HasPrefix(hit.Name, "perl"):
		return 0.55
	case hit.Tree == "x11" && hit.Section == "3":
		return 0.6
	case hit.Section == "3" || hit.Section == "9" || hit.Section == "2":
		return 0.85
	}
	return 1
}

func (kb *KnowledgeBase) ids(query string, args ...any) ([]int64, error) {
	rows, err := kb.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (kb *KnowledgeBase) fill(hit *Hit) {
	kb.db.QueryRow(`SELECT d.kind, d.name, d.section, d.heading, d.title,
		d.body, coalesce(p.tree, '') FROM docs d LEFT JOIN pages p
		ON p.kind = d.kind AND p.name = d.name AND p.section = d.section
		WHERE d.id = ?`, hit.ID).Scan(&hit.Kind, &hit.Name, &hit.Section,
		&hit.Heading, &hit.Title, &hit.Body, &hit.Tree)
}

// Page returns the sections of one man page, or one package's text.
// heading, when not empty, keeps only sections whose heading contains it.
func (kb *KnowledgeBase) Page(name, section, heading string) ([]Hit, error) {
	rows, err := kb.db.Query(`SELECT id, kind, name, section, heading, title,
		body FROM docs WHERE name = ? AND (? = '' OR section = ?)
		ORDER BY kind, section, id`, name, section, section)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hits []Hit
	firstSection := ""
	for rows.Next() {
		var hit Hit
		rows.Scan(&hit.ID, &hit.Kind, &hit.Name, &hit.Section, &hit.Heading,
			&hit.Title, &hit.Body)
		if firstSection == "" {
			firstSection = hit.Kind + hit.Section
		}
		if hit.Kind+hit.Section != firstSection { // man -s order: first wins
			continue
		}
		hits = append(hits, hit)
	}
	if heading != "" && len(hits) > 0 {
		hits = filterSections(hits, heading)
	}
	if len(hits) == 0 && section == "" {
		// Maybe an alias listed in NAME, like strlcat -> strlcpy(3).
		var real, realSection string
		err := kb.db.QueryRow(`SELECT name, section FROM pages
			WHERE kind = 'man' AND (' ' || aliases || ' ') LIKE ?`,
			"% "+name+" %").Scan(&real, &realSection)
		if err == nil && real != name {
			return kb.Page(real, realSection, heading)
		}
	}
	return hits, nil
}

// filterSections keeps the sections whose heading contains text; failing
// that, the sections whose body mentions every word of it (so "quick" finds
// where pf.conf(5) explains quick); failing that, the NAME section and a
// list of headings, so the model can ask again.
func filterSections(hits []Hit, text string) []Hit {
	upper := strings.ToUpper(text)
	var kept []Hit
	for _, hit := range hits {
		if strings.Contains(strings.ToUpper(hit.Heading), upper) {
			kept = append(kept, hit)
		}
	}
	if len(kept) > 0 {
		return kept
	}
	words := strings.Fields(strings.ToLower(text))
	for _, hit := range hits {
		body := strings.ToLower(hit.Body)
		all := true
		for _, word := range words {
			all = all && strings.Contains(body, word)
		}
		if all {
			kept = append(kept, hit)
		}
	}
	if len(kept) > 0 {
		return kept
	}
	var headings []string
	for _, hit := range hits {
		headings = append(headings, hit.Heading)
	}
	first := hits[0]
	first.Body = fmt.Sprintf("No section matches %q. Headings: %s",
		text, strings.Join(headings, ", "))
	return []Hit{first}
}

var ftsWord = regexp.MustCompile(`[\p{L}\p{N}_]+(?:[.\-][\p{L}\p{N}_]+)*`)

// ftsQuery turns free text into an FTS5 query: every word becomes a quoted
// term, joined with OR, so punctuation in the question cannot break the
// query syntax and BM25 does the ranking.
func ftsQuery(text string) string {
	seen := map[string]bool{}
	var terms []string
	for _, word := range ftsWord.FindAllString(strings.ToLower(text), -1) {
		if seen[word] || stopWords[word] {
			continue
		}
		seen[word] = true
		terms = append(terms, `"`+word+`"`)
	}
	return strings.Join(terms, " OR ")
}

var stopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "by": true, "can": true, "do": true, "does": true,
	"for": true, "from": true, "how": true, "i": true, "in": true,
	"is": true, "it": true, "me": true, "my": true, "of": true, "on": true,
	"or": true, "should": true, "so": true, "that": true, "the": true,
	"this": true, "to": true, "use": true, "what": true, "when": true,
	"where": true, "which": true, "why": true, "with": true, "you": true,
	"openbsd": true,
}

func placeholders(count int) string {
	if count == 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func anySlice(ids []int64) []any {
	values := make([]any, len(ids))
	for i, id := range ids {
		values[i] = id
	}
	return values
}

// formatHits renders search results for the model or the terminal.
func formatHits(hits []Hit, bodyChars int) string {
	if len(hits) == 0 {
		return "no matches\n"
	}
	var out strings.Builder
	for _, hit := range hits {
		out.WriteString(hitLabel(hit))
		if hit.Title != "" {
			out.WriteString(" — " + hit.Title)
		}
		out.WriteString("\n")
		body := hit.Body
		if bodyChars > 0 && len(body) > bodyChars {
			body = body[:bodyChars] + " …"
		}
		out.WriteString(body)
		out.WriteString("\n\n")
	}
	return out.String()
}

func hitLabel(hit Hit) string {
	if hit.Kind == "pkg" {
		return fmt.Sprintf("[pkg %s %s]", hit.Name, hit.Heading)
	}
	return fmt.Sprintf("[man %s(%s) %s]", hit.Name, hit.Section, hit.Heading)
}
