package sourceevents

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/mattn/go-sqlite3"
)

func TestRemoteHistoryReusesSceneOnlyWithinPublication(t *testing.T) {
	f, authority := runtimeFixture(t, Options{})
	sceneID := "ob:s:" + base64.RawURLEncoding.EncodeToString([]byte(`["snowluma","42","group","100"]`))
	raw := fmt.Sprintf(`{"id":%q,"adapterId":"snowluma","accountId":"42","kind":"group","platformSceneId":"100"}`, sceneID)
	scene := sceneFromValue(runtimeObject(t, raw))
	runtimePut(t, f.main, "scenes", sceneID, raw)
	if _, err := f.main.Exec("INSERT INTO recall_tombstones VALUES(?,?,?)", sceneID, "durable-recall", "synthetic"); err != nil {
		t.Fatal(err)
	}
	f.main.SetMaxOpenConns(1)
	conn, err := f.main.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var sceneReads, identityReads, recallReads int
	err = conn.Raw(func(raw any) error {
		raw.(*sqlite3.SQLiteConn).RegisterAuthorizer(func(op int, table, column, _ string) int {
			if op == sqlite3.SQLITE_READ {
				switch {
				case table == "scenes" && column == "value":
					sceneReads++
				case table == "config" && column == "value":
					identityReads++
				case table == "recall_tombstones" && column == "message_id":
					recallReads++
				}
			}
			return sqlite3.SQLITE_OK
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	message := func(scene, id, sender string) *jsjson.Object {
		return obj("id", id, "sceneId", scene, "senderId", sender, "parts", []any{}, "status", "available", "origin", "history")
	}
	for _, n := range []int{0, 2, 30} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			messages := []*jsjson.Object{message("foreign", "foreign", "external")}
			// A publication sees the current persisted identity even after a
			// prior call returned a snapshot for this same scene.
			name := fmt.Sprintf("Fixture Identity %d", n)
			runtimePut(t, f.main, "config", "identity:"+sceneID+":sender", fmt.Sprintf(`{"senderDisplayName":%q,"senderPlatformId":"123"}`, name))
			for i := range n {
				messages = append(messages, message(sceneID, strconv.Itoa(i), "sender"))
			}
			messages = append(messages, message(sceneID, "durable-recall", "sender"), message(sceneID, "local-recall", "sender"))
			sceneReads, identityReads, recallReads = 0, 0, 0
			candidateCalls := 0
			rows, links, err := authority.ProjectRemoteHistory(scene, messages, map[string]bool{"local-recall": true}, "", func(*jsjson.Object) botruntime.DisplayImageLinks {
				candidateCalls++
				return botruntime.DisplayImageLinks{}
			})
			if len(rows) != n || len(links) != 0 {
				t.Fatalf("wanted %d visible rows and no links; got %d / %d", n, len(rows), len(links))
			}
			for i, row := range rows {
				if str(row, "id") != strconv.Itoa(i) || str(row, "senderDisplayName") != name ||
					str(row, "senderPlatformId") != "123" || str(row, "senderAvatarUrl") != "https://q1.qlogo.cn/g?b=qq&nk=123&s=100" {
					t.Fatalf("visible presentation changed at %d: %#v", i, row)
				}
			}

			wantSceneReads := min(n, 1)
			if err != nil || sceneReads != wantSceneReads || identityReads != n || recallReads != n+1 || candidateCalls != n {
				t.Fatalf("scene=%d (want %d), identity=%d, recall=%d, candidate=%d; error=%v", sceneReads, wantSceneReads, identityReads, recallReads, candidateCalls, err)
			}
		})
	}
}
