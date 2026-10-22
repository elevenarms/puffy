# Build and install puffy on OpenBSD (BSD make or gmake).
#   pkg_add go sqlite3 llama.cpp gmake
#   make vector && make && doas make install

PREFIX ?=	/usr/local
GOFLAGS ?=	-mod=vendor -trimpath
TAGS =		sqlite_fts5
VECTOR_SRC ?=	../sqlite-vector

all: puffy

puffy: *.go go.mod
	go build ${GOFLAGS} -tags "${TAGS}" -o puffy .

# sqlite-vector's Makefile is GNU make; on OpenBSD it takes the "linux"
# branch (vector.so, -shared). cc is clang; -fPIC for a shared object.
vector:
	cd ${VECTOR_SRC} && gmake CC="cc -fPIC" CPUS=2 extension
	cp ${VECTOR_SRC}/dist/vector.so vector.so

install: puffy
	install -d ${DESTDIR}${PREFIX}/bin ${DESTDIR}${PREFIX}/lib/puffy \
	    ${DESTDIR}${PREFIX}/share/puffy
	install -m 755 puffy ${DESTDIR}${PREFIX}/bin/puffy
	install -m 644 vector.so ${DESTDIR}${PREFIX}/lib/puffy/vector.so
	install -m 644 data/puffy-kb.db ${DESTDIR}${PREFIX}/share/puffy/
	install -m 644 data/nomic-embed-text-v1.5.Q8_0.gguf \
	    ${DESTDIR}${PREFIX}/share/puffy/

clean:
	rm -f puffy vector.so

.PHONY: all vector install clean
