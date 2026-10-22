package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// runSearchCommand is puffy --search: the same search the model gets,
// printed for a person. Good for learning and for checking the index.
func runSearchCommand(s Settings, query string) int {
	if query == "" {
		fmt.Fprintln(os.Stderr, "usage: puffy --search query")
		return 1
	}
	kb, err := openKnowledgeBase(s)
	if err != nil {
		fmt.Fprintln(os.Stderr, "puffy: "+err.Error())
		return 1
	}
	defer kb.close()
	hits, err := kb.Search(query, "", 8)
	if err != nil {
		fmt.Fprintln(os.Stderr, "puffy: "+err.Error())
		return 1
	}
	paint := newPainter(s.TerminalOutput)
	for _, hit := range hits {
		how := ""
		if hit.Lexical > 0 {
			how += fmt.Sprintf(" keyword#%d", hit.Lexical)
		}
		if hit.Semantic > 0 {
			how += fmt.Sprintf(" vector#%d", hit.Semantic)
		}
		if hit.Page > 0 {
			how += fmt.Sprintf(" page#%d", hit.Page)
		}
		fmt.Printf("%s%s\n    %s\n", paint.action(hitLabel(hit)),
			paint.comment(how), hit.Title)
	}
	return 0
}

// runDoctor checks each part puffy needs and says how to fix what fails.
func runDoctor(s Settings) int {
	failures := 0
	paint := newPainter(s.TerminalOutput)
	check := func(name string, err error, detail string) {
		if err != nil {
			failures++
			fmt.Printf("%s %-15s %v\n", paint.bad("FAIL"), name, err)
			return
		}
		fmt.Printf("%s   %-15s %s\n", paint.good("ok"), name, detail)
	}

	check("api key", keyError(s), s.APIKeyFile)
	info, err := modelInfo(s)
	if isNetworkError(err) {
		fmt.Printf("%s   %-15s offline (%v); local search and man pages "+
			"still work\n", paint.notice("!!"), "model", err)
	} else {
		check("model", err, info)
	}

	kb, err := openKnowledgeBase(s)
	if err != nil {
		check("knowledge base", err, "")
		return 1
	}
	defer kb.close()
	var docs int
	kb.db.QueryRow("SELECT count(*) FROM docs").Scan(&docs)
	check("knowledge base", nil, fmt.Sprintf("%s: %d chunks, OpenBSD %s",
		s.KnowledgeBase, docs, kb.meta["openbsd_release"]))
	if kb.vectors {
		var backend string
		kb.db.QueryRow("SELECT vector_version() || ' ' || vector_backend()").
			Scan(&backend)
		check("sqlite-vector", nil, s.VectorExt+" "+backend)
		started := time.Now()
		_, err := kb.embedder.Embed(kb.prefix + "test")
		check("embedder", err, fmt.Sprintf("%s (first query %s)",
			s.EmbedModel, time.Since(started).Round(time.Millisecond)))
	} else {
		check("sqlite-vector", fmt.Errorf("not loaded from %s; set "+
			"PUFFY_VECTOR_EXT (keyword search still works)", s.VectorExt), "")
	}
	if failures > 0 {
		return 1
	}
	return 0
}

func keyError(s Settings) error {
	if s.APIKey == "" {
		return fmt.Errorf("missing; put it in %s or set PUFFY_API_KEY",
			s.APIKeyFile)
	}
	return nil
}

// modelInfo reads the model's privacy facts from the router's /models list.
func modelInfo(s Settings) (string, error) {
	request, _ := http.NewRequest("GET", s.APIBase+"/models", nil)
	request.Header.Set("Authorization", "Bearer "+s.APIKey)
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s from %s/models", response.Status, s.APIBase)
	}
	var list struct {
		Data []struct {
			ID     string `json:"id"`
			Router struct {
				Confidential bool   `json:"provider_confidential_compute"`
				Tier         string `json:"privacy_tier_label"`
			} `json:"trustedrouter"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&list); err != nil {
		return "", err
	}
	for _, model := range list.Data {
		if model.ID == s.Model {
			if !model.Router.Confidential ||
				!strings.Contains(model.Router.Tier, "Confidential") {
				return "", fmt.Errorf("%s is not confidential compute "+
					"(%s); set PUFFY_MODEL", s.Model, model.Router.Tier)
			}
			return s.Model + ": " + model.Router.Tier, nil
		}
	}
	return "", fmt.Errorf("%s not offered by %s; set PUFFY_MODEL", s.Model,
		s.APIBase)
}
