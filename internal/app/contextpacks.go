package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"git-ctx/internal/auth"
)

// Context packs: curated bundles an agent can pull in one call.

type contextPackInput struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     *bool  `json:"enabled"`
	// Purpose tells an agent how to read the pack, and the budget stops a large
	// one from spending the whole context on whichever repository is first.
	Purpose string `json:"purpose"`
	// TokenBudget is in bytes, matching the per-tool response budget. Zero means
	// the platform default.
	TokenBudget        int   `json:"tokenBudget"`
	IncludeConventions *bool `json:"includeConventions"`
	Items              []struct {
		LibraryID string `json:"libraryId"`
		Ref       string `json:"ref"`
		QueryHint string `json:"queryHint"`
	} `json:"items"`
	// Entrypoints are the symbols worth anchoring on -- the way in that a
	// maintainer would point at first.
	Entrypoints []struct {
		Symbol    string `json:"symbol"`
		LibraryID string `json:"libraryId"`
	} `json:"entrypoints"`
}

func (a *App) contextPacks(w http.ResponseWriter, r *http.Request) {
	rows, err := a.store.DB.QueryContext(r.Context(), `SELECT id,slug,name,description,enabled,created_by,created_at,updated_at,purpose,token_budget,include_conventions FROM context_packs ORDER BY name`)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	var packs []map[string]any
	for rows.Next() {
		var id, slug, name, description, createdBy, purpose string
		var enabled, tokenBudget, includeConventions int
		var createdAt, updatedAt time.Time
		if rows.Scan(&id, &slug, &name, &description, &enabled, &createdBy, &createdAt, &updatedAt, &purpose, &tokenBudget, &includeConventions) != nil {
			continue
		}
		packs = append(packs, map[string]any{"id": id, "slug": slug, "name": name, "description": description,
			"enabled": enabled != 0, "createdBy": createdBy, "createdAt": createdAt, "updatedAt": updatedAt,
			"purpose": purpose, "tokenBudget": tokenBudget, "includeConventions": includeConventions != 0})
	}
	rows.Close()
	for _, pack := range packs {
		itemRows, queryErr := a.store.DB.QueryContext(r.Context(), a.store.Rebind(`SELECT library_id,ref_name,query_hint FROM context_pack_items WHERE pack_id=? ORDER BY position,library_id`), pack["id"])
		if queryErr != nil {
			continue
		}
		var items []map[string]string
		for itemRows.Next() {
			var libraryID, ref, hint string
			if itemRows.Scan(&libraryID, &ref, &hint) == nil {
				items = append(items, map[string]string{"libraryId": libraryID, "ref": ref, "queryHint": hint})
			}
		}
		itemRows.Close()
		entryRows, entryErr := a.store.DB.QueryContext(r.Context(), a.store.Rebind(`SELECT symbol,library_id FROM context_pack_entrypoints WHERE pack_id=? ORDER BY position,symbol`), pack["id"])
		if entryErr == nil {
			var entrypoints []map[string]string
			for entryRows.Next() {
				var symbol, libraryID string
				if entryRows.Scan(&symbol, &libraryID) == nil {
					entrypoints = append(entrypoints, map[string]string{"symbol": symbol, "libraryId": libraryID})
				}
			}
			entryRows.Close()
			pack["entrypoints"] = entrypoints
		}
		pack["items"] = items
	}
	jsonOut(w, 200, packs)
}

func (a *App) createContextPack(w http.ResponseWriter, r *http.Request) {
	var input contextPackInput
	if decode(r, &input) != nil || strings.TrimSpace(input.Slug) == "" || strings.TrimSpace(input.Name) == "" {
		problem(w, 400, "invalid_request", "slug and name are required")
		return
	}
	id, err := randomToken(18)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	p, _ := auth.FromContext(r.Context())
	if err = a.saveContextPack(r.Context(), id, input, p.UserID, true); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			problem(w, 409, "duplicate", "Context pack slug already exists")
		} else {
			problem(w, 400, "invalid_request", err.Error())
		}
		return
	}
	jsonOut(w, 201, map[string]string{"id": id})
}

func (a *App) updateContextPack(w http.ResponseWriter, r *http.Request) {
	var input contextPackInput
	if decode(r, &input) != nil || strings.TrimSpace(input.Slug) == "" || strings.TrimSpace(input.Name) == "" {
		problem(w, 400, "invalid_request", "slug and name are required")
		return
	}
	p, _ := auth.FromContext(r.Context())
	if err := a.saveContextPack(r.Context(), r.PathValue("id"), input, p.UserID, false); err != nil {
		problem(w, 400, "invalid_request", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) saveContextPack(ctx context.Context, id string, input contextPackInput, userID string, create bool) error {
	if len(input.Items) == 0 || len(input.Items) > 50 {
		return errors.New("one to fifty context pack items are required")
	}
	for _, item := range input.Items {
		if baseLibraryID(item.LibraryID) == "" {
			return errors.New("each item requires a valid libraryId")
		}
	}
	if len(input.Entrypoints) > 50 {
		return errors.New("a context pack may name at most fifty entrypoints")
	}
	if input.TokenBudget != 0 && (input.TokenBudget < 4000 || input.TokenBudget > 1<<20) {
		return errors.New("tokenBudget must be 0 for the default, or 4000..1048576 bytes")
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	includeConventions := true
	if input.IncludeConventions != nil {
		includeConventions = *input.IncludeConventions
	}
	tx, err := a.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if create {
		_, err = tx.ExecContext(ctx, a.store.Rebind(`INSERT INTO context_packs(id,slug,name,description,enabled,created_by,purpose,token_budget,include_conventions) VALUES(?,?,?,?,?,?,?,?,?)`), id, strings.ToLower(strings.TrimSpace(input.Slug)), strings.TrimSpace(input.Name), strings.TrimSpace(input.Description), enabled, userID, strings.TrimSpace(input.Purpose), input.TokenBudget, boolInt(includeConventions))
	} else {
		var result sql.Result
		result, err = tx.ExecContext(ctx, a.store.Rebind(`UPDATE context_packs SET slug=?,name=?,description=?,enabled=?,purpose=?,token_budget=?,include_conventions=?,updated_at=? WHERE id=?`), strings.ToLower(strings.TrimSpace(input.Slug)), strings.TrimSpace(input.Name), strings.TrimSpace(input.Description), enabled, strings.TrimSpace(input.Purpose), input.TokenBudget, boolInt(includeConventions), time.Now().UTC(), id)
		if err == nil {
			if count, _ := result.RowsAffected(); count == 0 {
				return errors.New("context pack not found")
			}
		}
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, a.store.Rebind(`DELETE FROM context_pack_items WHERE pack_id=?`), id); err != nil {
		return err
	}
	for position, item := range input.Items {
		if _, err = tx.ExecContext(ctx, a.store.Rebind(`INSERT INTO context_pack_items(pack_id,library_id,ref_name,query_hint,position) VALUES(?,?,?,?,?)`), id, baseLibraryID(item.LibraryID), strings.TrimSpace(item.Ref), strings.TrimSpace(item.QueryHint), position); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, a.store.Rebind(`DELETE FROM context_pack_entrypoints WHERE pack_id=?`), id); err != nil {
		return err
	}
	for position, entry := range input.Entrypoints {
		symbol := strings.TrimSpace(entry.Symbol)
		if symbol == "" {
			continue
		}
		if _, err = tx.ExecContext(ctx, a.store.Rebind(`INSERT INTO context_pack_entrypoints(pack_id,symbol,library_id,position) VALUES(?,?,?,?)`),
			id, symbol, baseLibraryID(entry.LibraryID), position); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *App) deleteContextPack(w http.ResponseWriter, r *http.Request) {
	result, err := a.store.DB.ExecContext(r.Context(), a.store.Rebind(`DELETE FROM context_packs WHERE id=?`), r.PathValue("id"))
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	if count, _ := result.RowsAffected(); count == 0 {
		problem(w, 404, "not_found", "Context pack not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
