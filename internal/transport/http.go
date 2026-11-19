// Package transport exposes bounded JSON and binary rendering workflows.
package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"prismplate/internal/exposure"
	"prismplate/internal/repository"
	"prismplate/internal/scene"
	"prismplate/internal/service"
	"strconv"
	"time"
)

type API struct{ Service *service.Service }

func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, err error) {
	status := 400
	switch {
	case errors.Is(err, repository.ErrMissing), errors.Is(err, exposure.ErrMissing):
		status = 404
	case errors.Is(err, repository.ErrConflict), errors.Is(err, repository.ErrState):
		status = 409
	case errors.Is(err, exposure.ErrBusy):
		status = 429
	case errors.Is(err, context.DeadlineExceeded):
		status = 504
	case errors.Is(err, context.Canceled):
		status = 408
	}
	jsonOut(w, status, map[string]string{"error": err.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	if e := d.Decode(new(any)); e != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}
func revision(r *http.Request) (int, error) {
	n, e := strconv.Atoi(r.URL.Query().Get("revision"))
	if e != nil || n < 1 {
		return 0, fmt.Errorf("positive revision required")
	}
	return n, nil
}
func (a API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, 200, map[string]string{"service": "prism-plate", "state": "ready"})
	})
	mux.HandleFunc("POST /v1/fonts", func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			ID   string `json:"id"`
			Data []byte `json:"data"`
		}
		if e := decode(w, r, &q); e != nil {
			failure(w, e)
			return
		}
		v, e := a.Service.AddFont(q.ID, q.Data)
		if e != nil {
			failure(w, e)
			return
		}
		v.Data = nil
		jsonOut(w, 201, v)
	})
	mux.HandleFunc("GET /v1/fonts/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, e := a.Service.Font(r.PathValue("id"))
		if e != nil {
			failure(w, e)
			return
		}
		v.Data = nil
		jsonOut(w, 200, v)
	})
	mux.HandleFunc("POST /v1/plates", func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			ID       string         `json:"id"`
			Title    string         `json:"title"`
			Document scene.Document `json:"document"`
		}
		if e := decode(w, r, &q); e != nil {
			failure(w, e)
			return
		}
		p, e := a.Service.Create(q.ID, q.Title, q.Document)
		if e != nil {
			failure(w, e)
			return
		}
		jsonOut(w, 201, p)
	})
	mux.HandleFunc("GET /v1/plates/{id}", func(w http.ResponseWriter, r *http.Request) {
		p, e := a.Service.Plate(r.PathValue("id"))
		if e != nil {
			failure(w, e)
			return
		}
		jsonOut(w, 200, p)
	})
	mux.HandleFunc("PUT /v1/plates/{id}", func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Expected int            `json:"expected_revision"`
			Document scene.Document `json:"document"`
		}
		if e := decode(w, r, &q); e != nil {
			failure(w, e)
			return
		}
		p, e := a.Service.Revise(r.PathValue("id"), q.Expected, q.Document)
		if e != nil {
			failure(w, e)
			return
		}
		jsonOut(w, 200, p)
	})
	mux.HandleFunc("POST /v1/plates/{id}/branch", func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Revision int    `json:"revision"`
			ID       string `json:"id"`
			Title    string `json:"title"`
		}
		if e := decode(w, r, &q); e != nil {
			failure(w, e)
			return
		}
		p, e := a.Service.Branch(r.PathValue("id"), q.Revision, q.ID, q.Title)
		if e != nil {
			failure(w, e)
			return
		}
		jsonOut(w, 201, p)
	})
	mux.HandleFunc("POST /v1/plates/{id}/archive", func(w http.ResponseWriter, r *http.Request) {
		if e := a.Service.Archive(r.PathValue("id")); e != nil {
			failure(w, e)
			return
		}
		jsonOut(w, 200, map[string]string{"state": "archived"})
	})
	mux.HandleFunc("POST /v1/plates/{id}/render", func(w http.ResponseWriter, r *http.Request) {
		n, e := revision(r)
		if e != nil {
			failure(w, e)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		v, e := a.Service.Render(ctx, r.PathValue("id"), n, r.URL.Query().Get("format"))
		if e != nil {
			failure(w, e)
			return
		}
		v.Data = nil
		jsonOut(w, 201, v)
	})
	mux.HandleFunc("POST /v1/plates/{id}/exposures", func(w http.ResponseWriter, r *http.Request) {
		n, e := revision(r)
		if e != nil {
			failure(w, e)
			return
		}
		v, e := a.Service.Submit(r.PathValue("id"), n, r.URL.Query().Get("format"))
		if e != nil {
			failure(w, e)
			return
		}
		jsonOut(w, 202, v)
	})
	mux.HandleFunc("GET /v1/exposures/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, e := a.Service.Pool.Get(r.PathValue("id"))
		if e != nil {
			failure(w, e)
			return
		}
		jsonOut(w, 200, v)
	})
	mux.HandleFunc("DELETE /v1/exposures/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, e := a.Service.Pool.Cancel(r.PathValue("id"))
		if e != nil {
			failure(w, e)
			return
		}
		jsonOut(w, 202, v)
	})
	mux.HandleFunc("GET /v1/artifacts/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, e := a.Service.Artifact(r.PathValue("id"))
		if e != nil {
			failure(w, e)
			return
		}
		w.Header().Set("ETag", `"`+v.Digest+`"`)
		w.Header().Set("X-Scene-Digest", v.SceneDigest)
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
		w.Header().Set("Content-Type", map[string]string{"png": "image/png", "svg": "image/svg+xml"}[v.Format])
		if r.Header.Get("If-None-Match") == `"`+v.Digest+`"` {
			w.WriteHeader(304)
			return
		}
		_, _ = w.Write(v.Data)
	})
	mux.HandleFunc("DELETE /v1/artifacts/{id}", func(w http.ResponseWriter, r *http.Request) {
		if e := a.Service.Revoke(r.PathValue("id")); e != nil {
			failure(w, e)
			return
		}
		jsonOut(w, 200, map[string]string{"state": "revoked"})
	})
	mux.HandleFunc("POST /v1/proofs", func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			ID       string `json:"id"`
			Artifact string `json:"artifact"`
		}
		if e := decode(w, r, &q); e != nil {
			failure(w, e)
			return
		}
		v, e := a.Service.OpenProof(q.ID, q.Artifact)
		if e != nil {
			failure(w, e)
			return
		}
		jsonOut(w, 201, v)
	})
	mux.HandleFunc("PUT /v1/proofs/{id}", func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			State string `json:"state"`
			Note  string `json:"note"`
		}
		if e := decode(w, r, &q); e != nil {
			failure(w, e)
			return
		}
		v, e := a.Service.Decide(r.PathValue("id"), q.State, q.Note)
		if e != nil {
			failure(w, e)
			return
		}
		jsonOut(w, 200, v)
	})
	mux.HandleFunc("GET /v1/plates/{id}/bundle", func(w http.ResponseWriter, r *http.Request) {
		v, e := a.Service.Export(r.PathValue("id"))
		if e != nil {
			failure(w, e)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(v)
	})
	mux.HandleFunc("POST /v1/bundles", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
		b, e := io.ReadAll(r.Body)
		if e != nil {
			failure(w, e)
			return
		}
		v, e := a.Service.Import(b)
		if e != nil {
			failure(w, e)
			return
		}
		jsonOut(w, 201, v)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}
