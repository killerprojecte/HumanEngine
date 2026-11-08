package commitments

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"strings"
	"testing"
)

func TestCommandOwnerExpiryFallbackAndTransactionLoss(t *testing.T) {
	for _, mode := range []string{"expiry", "transaction-loss", "cancelled-owner", "wrong-child"} {
		t.Run(mode, func(t *testing.T) {
			f, as, options := currentFixture(t)
			ctx := context.Background()
			input := obj("sceneId", "scene", "simulation", false, "provenance", "source-only")
			command := obj("kind", "propose", "terms", obj("kind", "plan", "title", "Synthetic sender fallback", "participantIds", []any{"a"}, "temporalScope", "future", "polarity", "positive", "modality", "asserted", "conditions", []any{}))
			backing := referenceValue(Reference{ID: "root", Version: 1})
			var escaped *CommandService
			owners := f.service.owners
			before, e := f.ledger.Head(ctx)
			must(t, e)
			initial := stringify(t, tableValue(t, f))
			var firstLoss, afterLoss, writeAfterLoss error
			if mode == "transaction-loss" {
				_, e := f.main.Exec(`CREATE TABLE command_loss_probe(value TEXT); CREATE TRIGGER command_native_loss BEFORE INSERT ON memory_commitment_commands BEGIN SELECT RAISE(ROLLBACK,'synthetic_command_native_loss'); END`)
				must(t, e)
			}

			var cancel context.CancelFunc
			if mode == "cancelled-owner" {
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				owners.AfterWrite = func(*Store, *Object, []any, []any) error { cancel(); return nil }
			}
			e = f.store.Transaction(ctx, func(st *Store) error {
				if mode == "wrong-child" {
					return st.Transaction(ctx, func(child *Store) error {
						_, e := st.BindCommandSession(child.session)
						if e == nil || !strings.Contains(e.Error(), "session_binding") {
							t.Fatalf("rebound ancestor: %v", e)
						}
						return nil
					})
				}
				return st.WithCommands(ctx, as.WithSession(st.session), currentEnabled, options, owners, func(service *CommandService) error {
					escaped = service
					result, e := service.Admit(input, backing, command, CommandPolicy{LearningEnabled: true, ID: func() (string, error) { return "sender-fallback", nil }})
					if mode == "expiry" && (e != nil || result.Status != "saved" || get(result.Item, "confirmation") != "confirmed") {
						t.Fatalf("source sender fallback rejected: %v %v", result, e)
					}
					if mode == "transaction-loss" {
						firstLoss = e
						_, afterLoss = service.Admit(input, backing, command, CommandPolicy{LearningEnabled: true})
						_, writeAfterLoss = st.session.Exec(`INSERT INTO command_loss_probe VALUES('must-not-autocommit')`)
						return nil // Even deliberately caught native loss cannot restore ownership.
					}
					return e
				})
			})
			if mode == "transaction-loss" {
				if firstLoss == nil || !strings.Contains(firstLoss.Error(), "synthetic_command_native_loss") || !errors.Is(afterLoss, sql.ErrTxDone) || !errors.Is(writeAfterLoss, sql.ErrTxDone) || e == nil {
					t.Fatalf("native loss: first=%v next=%v write=%v root=%v", firstLoss, afterLoss, writeAfterLoss, e)
				}
				if initial != stringify(t, tableValue(t, f)) {
					t.Fatal("native rollback leaked command prefix")
				}
				var n int
				must(t, f.main.QueryRow("SELECT count(*) FROM command_loss_probe").Scan(&n))
				if n != 0 {
					t.Fatal("post-loss autocommit")
				}
				head, err := f.ledger.Head(context.Background())
				must(t, err)
				gate, err := recovery.ReadGate(context.Background(), f.main)
				must(t, err)
				if head.Sequence != before.Sequence+1 || gate.Head != before {
					t.Fatal("native loss lost intent or wrote ACK")
				}
				return
			}
			if mode == "cancelled-owner" {
				if e == nil {
					t.Fatal("cancelled owner accepted")
				}
				return
			}
			must(t, e)
			if mode == "expiry" {
				_, e := escaped.Admit(input, backing, command, CommandPolicy{LearningEnabled: true})
				if !errors.Is(e, sql.ErrTxDone) {
					t.Fatalf("expired command owner: %v", e)
				}
			}
		})
	}
}

func TestCommandPeopleActualAuthorityTrustedContext(t *testing.T) {
	f, as, options := currentFixture(t)
	evidence := func(available, bot bool, people ...string) *Object {
		mentions := []any{}
		for _, p := range people {
			mentions = append(mentions, obj("userId", p, "isBot", p == "bot-mention"))
		}
		return obj("sceneId", "scene", "messageId", "root", "senderId", "a", "available", available, "isBot", bot, "mentions", mentions)
	}
	cases := []struct {
		name     string
		evidence []any
		want     []any
	}{
		{"absent trusted evidence still yields sender", nil, []any{"a"}},
		{"unavailable and bot evidence ignored", []any{evidence(false, false, "unavailable"), evidence(true, true, "bot-evidence")}, []any{"a"}},
		{"first available nonbot match only", []any{evidence(false, false, "unavailable"), evidence(true, true, "bot-evidence"), evidence(true, false, "b", "bot-mention", "a"), evidence(true, false, "c")}, []any{"a", "b"}},
		{"nonmatching trusted source excluded", []any{obj("sceneId", "other", "messageId", "root", "senderId", "a", "available", true, "isBot", false, "mentions", []any{obj("userId", "outsider", "isBot", false)})}, []any{"a"}},
	}
	must(t, f.store.Transaction(context.Background(), func(st *Store) error {
		return st.WithCommands(context.Background(), as.WithSession(st.session), currentEnabled, options, f.service.owners, func(service *CommandService) error {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					input := obj("sourceEvidence", tc.evidence)
					people, e := service.people(Context{SceneID: "scene"}, Reference{ID: "root", Version: 1}, input)
					must(t, e)
					if !deepEqual(people, tc.want) {
						t.Fatalf("trusted people got %v want %v", people, tc.want)
					}
				})
			}
			return nil
		})
	}))
}
