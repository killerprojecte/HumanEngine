// Package offlinelifecycle composes quarantined recovery through real native
// domain owners. It is not a live message replay or a production boot path.
package offlinelifecycle

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/externalcost"
	"github.com/killerprojecte/HumanEngine/backend/internal/identityprivacy"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/recoverydomains"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
	"time"
)

type Options struct {
	Memory    memoryservice.OfflineLifecycleOptions
	MediaRoot string
	Now       func() time.Time
}

// Reconcile runs the exact audited authority in source order. Callers must stop
// all staging writers and retain exclusive connection ownership. No failure is
// retried here: previously committed SQL/file effects remain quarantined and
// the next attempt revalidates actual material against the original prefix.
func Reconcile(ctx context.Context, owner *recovery.Maintenance, options Options) (recovery.Head, error) {
	zero := recovery.Head{}
	if owner == nil {
		return zero, errors.New("recovery_maintenance_required")
	}
	entries, e := owner.History(ctx)
	if e != nil {
		return zero, e
	}
	if e = owner.PrepareReconciliationConnection(ctx); e != nil {
		return zero, e
	}
	events := make([]string, len(entries))
	for i, v := range entries {
		events[i] = v.Payload
	}
	eligibility, e := newEligibility(owner, entries)
	if e != nil {
		return zero, e
	}
	if e = owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error { return tx.AssertSecurityState(ctx, entries) }); e != nil {
		return zero, e
	}
	if e = externalcost.ReplayOffline(ctx, externalcost.NewOfflineMaintenance(owner), events); e != nil {
		return zero, e
	}
	memory, e := memoryservice.NewOfflineLifecycle(owner, options.Memory)
	if e != nil {
		return zero, e
	}
	working, e := workingset.NewOfflineMaintenance(owner, workingset.OfflinePorts{ResolvePinTarget: memory.ResolvePinTarget})
	if e != nil {
		return zero, e
	}
	if _, e = working.AssertSupported(ctx); e != nil {
		return zero, e
	}
	sources := &sourceReplay{owner}
	if e = sources.AssertSupported(ctx); e != nil {
		return zero, e
	}
	commitments, e := memory.CommitmentRecovery(ctx)
	if e != nil {
		return zero, e
	}
	if _, e = commitments.AssertSupported(ctx); e != nil {
		return zero, e
	}
	domainOptions := recoverydomains.Options{Compare: options.Memory.Authority.Compare, Now: options.Now}
	trials, e := recoverydomains.NewTrialsWithOptions(owner, domainOptions)
	if e != nil {
		return zero, e
	}
	if e = trials.AssertSupported(ctx); e != nil {
		return zero, e
	}
	if e = trials.Prepare(ctx, events); e != nil {
		return zero, e
	}
	domains, e := recoverydomains.NewWithOptions(owner, options.MediaRoot, domainOptions)
	if e != nil {
		return zero, e
	}
	if e = domains.AssertSupported(ctx); e != nil {
		return zero, e
	}
	identities, e := identityprivacy.NewOfflineRegistry(owner)
	if e != nil {
		return zero, e
	}
	if e = identities.Replay(ctx, entries, owner.SnapshotSequence()); e != nil {
		return zero, e
	}
	semantic := &semanticReplay{owner: owner, memory: memory}
	if e = semantic.Prepare(ctx, events); e != nil {
		return zero, e
	}
	knowledge := &knowledgeReplay{owner}
	if e = knowledge.AssertSupported(ctx); e != nil {
		return zero, e
	}
	if e = owner.InitializeOfflineMemory(ctx); e != nil {
		return zero, e
	}
	affected := map[string]bool{}
	receipts := map[string]int{}
	capture := func(effects []string, err error) error {
		if err != nil {
			return err
		}
		for _, raw := range effects {
			v, e := event(raw)
			if e != nil {
				return e
			}
			if get(v, "kind") != "assertion" || get(v, "terminal") == true {
				continue
			}
			key, e := redactionKey(v)
			if e != nil {
				return e
			}
			receipts[key]++
		}
		return nil
	}
	applied := []string{}
	for _, entry := range entries {
		if e = owner.CheckHead(ctx); e != nil {
			return zero, e
		}
		v, e := event(entry.Payload)
		if e != nil {
			return zero, e
		}
		suffix := entry.Sequence > owner.SnapshotSequence()
		switch get(v, "kind") {
		case "external_cost_receipt", "identity_mutation", "working_set_pin", "security_state", "source_edit_head":
			continue
		case "assertion_eligibility":
			e = eligibility.Apply(ctx, entry)
		case "commitment_head":
			if suffix {
				e = commitments.ReplayMarker(ctx, entry.Payload)
			}
		case "experience_trial":
			if suffix {
				e = trials.Apply(ctx, entry.Payload)
			}
		case "semantic_mutation":
			if suffix {
				e = semantic.Apply(ctx, entry.Payload)
			}
		case "commitment_delete":
			e = commitments.RecoverErase(ctx, entry.Payload)
		case "configuration_flags", "scene_flags":
			if suffix {
				e = replayConfiguration(ctx, owner, v)
			}
		case "source":
			receipts = map[string]int{}
			e = capture(memory.Recall(ctx, str(get(v, "sceneId")), str(get(v, "messageId"))))
			if e == nil {
				e = sources.Recall(ctx, str(get(v, "sceneId")), str(get(v, "messageId")))
			}
			if e == nil {
				e = commitments.Recall(ctx, str(get(v, "sceneId")), str(get(v, "messageId")))
			}
			if e == nil {
				e = domains.Apply(ctx, entry.Payload)
				if e == nil {
					applied = append(applied, entry.Payload)
				}
			}
		case "fingerprint":
			e = capture(memory.DeleteFingerprint(ctx, str(get(v, "value"))))
		case "assertion":
			if get(v, "terminal") == true {
				id := str(get(v, "id"))
				e = commitments.PurgeAssertion(ctx, id)
				if e == nil {
					affected[id] = true
					e = capture(memory.EraseRecoveredAssertion(ctx, id))
				}
			} else if suffix {
				key, keyErr := redactionKey(v)
				if keyErr != nil {
					return zero, keyErr
				}
				if count := receipts[key]; count > 0 {
					if count == 1 {
						delete(receipts, key)
					} else {
						receipts[key] = count - 1
					}
					continue
				}
				receipts = map[string]int{}
				e = capture(memory.RedactRecoveredHistory(ctx, str(get(v, "id"))))
			}
		default:
			if suffix {
				e = domains.Apply(ctx, entry.Payload)
				if e == nil {
					applied = append(applied, entry.Payload)
				}
			}
		}
		if e != nil {
			return zero, e
		}
	}
	return owner.FinalizeVerified(ctx, func(tx *recovery.MaintenanceTx) error {
		for _, raw := range events {
			v, e := event(raw)
			if e != nil {
				return e
			}
			var remains bool
			if get(v, "kind") == "assertion" && affected[str(get(v, "id"))] {
				e = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM memories WHERE id=?)", get(v, "id")).Scan(&remains)
			}
			if get(v, "kind") == "source" {
				e = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM memory_sources WHERE scene_id=? AND message_id=?)", get(v, "sceneId"), get(v, "messageId")).Scan(&remains)
			}
			if e != nil {
				return e
			}
			if remains {
				return errors.New("recovery_purge_incomplete")
			}
		}
		if e := eligibility.Verify(ctx, tx); e != nil {
			return e
		}
		if e := trials.Verify(ctx, tx, events); e != nil {
			return e
		}
		if e := semantic.Verify(ctx, tx, events); e != nil {
			return e
		}
		if e := knowledge.Verify(ctx, tx, events); e != nil {
			return e
		}
		if e := identities.Verify(ctx, tx, events); e != nil {
			return e
		}
		if e := domains.Verify(ctx, tx, applied); e != nil {
			return e
		}
		if e := sources.Verify(ctx, tx, events); e != nil {
			return e
		}
		if e := commitments.WithScope(ctx, tx).Verify(ctx, events); e != nil {
			return e
		}
		bound := working.WithScope(ctx, tx)
		if e := bound.VerifyPinsInScope(ctx, events); e != nil {
			return e
		}
		return bound.ResetInScope(ctx)
	})
}
func redactionKey(v *object) (string, error) {
	return jsjson.Stringify([]any{get(v, "kind"), get(v, "id"), get(v, "throughVersion"), get(v, "terminal")})
}
