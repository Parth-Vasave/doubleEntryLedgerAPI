// Command webhook-sink is an example webhook receiver for local demos. It
// logs every delivery and, when WEBHOOK_SECRET is set, verifies signatures
// exactly as a production receiver should.
package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/webhook"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	secret := os.Getenv("WEBHOOK_SECRET")
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8090"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		if secret != "" {
			if err := webhook.Verify(secret, r.Header.Get(webhook.SignatureHeader), body, 5*time.Minute, time.Now()); err != nil {
				log.Warn("rejected delivery", "error", err)
				http.Error(w, "bad signature", http.StatusUnauthorized)
				return
			}
		}
		var env webhook.Envelope
		_ = json.Unmarshal(body, &env)
		log.Info("webhook received", "event_id", env.ID, "type", env.Type, "attempt", env.Attempt,
			"verified", secret != "")
		w.WriteHeader(http.StatusNoContent)
	})
	log.Info("webhook sink listening", "addr", addr)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Error("server", "error", err)
		os.Exit(1)
	}
}
