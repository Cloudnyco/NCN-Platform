// wikistore.go — Postgres-backed store for the self-hosted wiki (see
// migrations/0015). Pages are markdown; rendering happens on the frontend.
// Everything tolerates globalDB == nil (wiki simply reports unavailable, like
// every other DB-optional store). The is_public flag is the hard boundary
// between the anonymous tier and the operator-only tier — public reads ALWAYS
// pass publicOnly=true so internal pages can never leak through a public route.
package main

import (
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

var errWikiNoDB = errors.New("wiki requires the database")

// wikiPage is a full page (with content). wikiMeta is the lightweight row for
// the tree/nav (no content). wikiVersion is one history snapshot.
type wikiPage struct {
	Path      string    `json:"path"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	Locale    string    `json:"locale"`
	IsPublic  bool      `json:"is_public"`
	Sort      int       `json:"sort"`
	UpdatedBy string    `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
	Version   int       `json:"version"`
}

type wikiMeta struct {
	Path      string    `json:"path"`
	Title     string    `json:"title"`
	IsPublic  bool      `json:"is_public"`
	Sort      int       `json:"sort"`
	UpdatedBy string    `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

type wikiVersion struct {
	ID       int64     `json:"id"`
	Path     string    `json:"path"`
	Title    string    `json:"title"`
	Content  string    `json:"content,omitempty"`
	Version  int       `json:"version"`
	EditedBy string    `json:"edited_by"`
	EditedAt time.Time `json:"edited_at"`
}

type wikiHit struct {
	Path    string `json:"path"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
}

type wikiStore struct{}

var globalWiki = &wikiStore{}

// list returns the page tree (metadata only), sorted for nav.
func (s *wikiStore) list(publicOnly bool) ([]wikiMeta, error) {
	if globalDB == nil {
		return nil, errWikiNoDB
	}
	q := `SELECT path, title, is_public, sort, updated_by, updated_at FROM wiki_pages`
	if publicOnly {
		q += ` WHERE is_public = true`
	}
	q += ` ORDER BY sort, path`
	rows, err := globalDB.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []wikiMeta
	for rows.Next() {
		var m wikiMeta
		if err := rows.Scan(&m.Path, &m.Title, &m.IsPublic, &m.Sort, &m.UpdatedBy, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// get returns one page. publicOnly=true refuses internal pages (returns false).
func (s *wikiStore) get(path string, publicOnly bool) (*wikiPage, bool, error) {
	if globalDB == nil {
		return nil, false, errWikiNoDB
	}
	q := `SELECT path, title, content, locale, is_public, sort, updated_by, updated_at, version FROM wiki_pages WHERE path = $1`
	if publicOnly {
		q += ` AND is_public = true`
	}
	var p wikiPage
	err := globalDB.QueryRow(q, path).Scan(&p.Path, &p.Title, &p.Content, &p.Locale, &p.IsPublic, &p.Sort, &p.UpdatedBy, &p.UpdatedAt, &p.Version)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &p, true, nil
}

// upsert creates or updates a page. On update it snapshots the PREVIOUS content
// into wiki_page_versions and bumps the version. All in one transaction.
func (s *wikiStore) upsert(p wikiPage, actor string) (*wikiPage, error) {
	if globalDB == nil {
		return nil, errWikiNoDB
	}
	tx, err := globalDB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var prevTitle, prevContent string
	var prevVersion int
	existed := true
	err = tx.QueryRow(`SELECT title, content, version FROM wiki_pages WHERE path=$1`, p.Path).Scan(&prevTitle, &prevContent, &prevVersion)
	if err == sql.ErrNoRows {
		existed = false
	} else if err != nil {
		return nil, err
	}

	newVersion := 1
	if existed {
		// snapshot the previous state for history/revert
		if _, err := tx.Exec(`INSERT INTO wiki_page_versions(path, title, content, version, edited_by, edited_at)
			VALUES ($1,$2,$3,$4,$5, now())`, p.Path, prevTitle, prevContent, prevVersion, actor); err != nil {
			return nil, err
		}
		newVersion = prevVersion + 1
		if _, err := tx.Exec(`UPDATE wiki_pages SET title=$2, content=$3, locale=$4, is_public=$5, sort=$6,
			updated_by=$7, updated_at=now(), version=$8 WHERE path=$1`,
			p.Path, p.Title, p.Content, p.Locale, p.IsPublic, p.Sort, actor, newVersion); err != nil {
			return nil, err
		}
	} else {
		if _, err := tx.Exec(`INSERT INTO wiki_pages(path, title, content, locale, is_public, sort, updated_by, updated_at, version, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7, now(), 1, now())`,
			p.Path, p.Title, p.Content, p.Locale, p.IsPublic, p.Sort, actor); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	p.Version, p.UpdatedBy = newVersion, actor
	p.UpdatedAt = time.Now().UTC()
	return &p, nil
}

func (s *wikiStore) delete(path string) (bool, error) {
	if globalDB == nil {
		return false, errWikiNoDB
	}
	res, err := globalDB.Exec(`DELETE FROM wiki_pages WHERE path=$1`, path)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// versions lists history snapshots (newest first), content omitted for the list.
func (s *wikiStore) versions(path string) ([]wikiVersion, error) {
	if globalDB == nil {
		return nil, errWikiNoDB
	}
	rows, err := globalDB.Query(`SELECT id, path, title, version, edited_by, edited_at
		FROM wiki_page_versions WHERE path=$1 ORDER BY id DESC LIMIT 100`, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []wikiVersion
	for rows.Next() {
		var v wikiVersion
		if err := rows.Scan(&v.ID, &v.Path, &v.Title, &v.Version, &v.EditedBy, &v.EditedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// revert restores a historical version's content/title as a new current version.
func (s *wikiStore) revert(path string, versionID int64, actor string) (*wikiPage, error) {
	if globalDB == nil {
		return nil, errWikiNoDB
	}
	var v wikiVersion
	err := globalDB.QueryRow(`SELECT title, content FROM wiki_page_versions WHERE id=$1 AND path=$2`, versionID, path).
		Scan(&v.Title, &v.Content)
	if err == sql.ErrNoRows {
		return nil, errors.New("version not found")
	}
	if err != nil {
		return nil, err
	}
	cur, ok, err := s.get(path, false)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("page not found")
	}
	cur.Title, cur.Content = v.Title, v.Content
	return s.upsert(*cur, actor)
}

// search does a simple case-insensitive match over title + content.
func (s *wikiStore) search(q string, publicOnly bool) ([]wikiHit, error) {
	if globalDB == nil {
		return nil, errWikiNoDB
	}
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	like := "%" + q + "%"
	sqlq := `SELECT path, title, content FROM wiki_pages WHERE (title ILIKE $1 OR content ILIKE $1)`
	if publicOnly {
		sqlq += ` AND is_public = true`
	}
	sqlq += ` ORDER BY sort, path LIMIT 50`
	rows, err := globalDB.Query(sqlq, like)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []wikiHit
	for rows.Next() {
		var path, title, content string
		if err := rows.Scan(&path, &title, &content); err != nil {
			return nil, err
		}
		out = append(out, wikiHit{Path: path, Title: title, Snippet: wikiSnippet(content, q)})
	}
	return out, rows.Err()
}

// wikiSnippet returns a short context window around the first match of q.
// Windowing is done on RUNES, not bytes: the content is UTF-8 (mostly CJK, 3
// bytes/char), so slicing by byte offset cut characters in half and produced
// U+FFFD (�) in the snippet. Rune slicing keeps every character intact.
func wikiSnippet(content, q string) string {
	const (
		before  = 40  // runes of left context
		after   = 80  // runes of right context
		nomatch = 120 // runes shown when q isn't found in the body
	)
	runes := []rune(content)
	lc, lq := strings.ToLower(content), strings.ToLower(q)
	bi := strings.Index(lc, lq) // byte offset (ToLower preserves byte offsets for ASCII+CJK)
	if bi < 0 {
		if len(runes) > nomatch {
			return strings.TrimSpace(string(runes[:nomatch])) + "…"
		}
		return strings.TrimSpace(content)
	}
	ri := utf8.RuneCountInString(content[:bi]) // byte offset → rune index
	rq := utf8.RuneCountInString(q)
	start := ri - before
	if start < 0 {
		start = 0
	}
	end := ri + rq + after
	if end > len(runes) {
		end = len(runes)
	}
	snip := strings.TrimSpace(string(runes[start:end]))
	if start > 0 {
		snip = "…" + snip
	}
	if end < len(runes) {
		snip = snip + "…"
	}
	return snip
}
