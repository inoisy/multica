package handler

import (
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestSubscriptionCapsRejectInvalidValuesAndUnknownWindows(t *testing.T) {
	h := subscriptionTestHandler(t)
	created := createSubtestSubscription(t, h, map[string]any{"key": "subtest-cap-validation", "provider": "claude", "caps": map[string]int{"5h": 80}})
	for _, caps := range []any{nil, map[string]any{"5h": nil}, map[string]any{"bogus": 50}, map[string]any{"5h": -1}, map[string]any{"5h": 101}, map[string]any{"5h": 50.5}, map[string]any{"5h": "80"}} {
		body := map[string]any{"key": "subtest-invalid-cap", "provider": "claude", "caps": caps}
		testutil.Call(t, h.CreateSubscription, newRequest("POST", "/api/subscriptions", body)).Want(http.StatusBadRequest)
		testutil.Call(t, h.UpdateSubscription, withURLParam(newRequest("PATCH", "/", map[string]any{"caps": caps}), "key", "subtest-cap-validation")).Want(http.StatusBadRequest)
		var stored []byte
		dbfx.QueryRow(t, "SELECT caps FROM subscription WHERE id=$1", created["id"]).Scan(&stored)
		if decodeCaps(stored)["5h"] != 80 {
			t.Fatalf("rejected caps=%v changed saved cap: %s", caps, stored)
		}
	}
	for _, cap := range []int{0, 100} {
		var out SubscriptionResponse
		testutil.Call(t, h.UpdateSubscription, withURLParam(newRequest("PATCH", "/", map[string]any{"caps": map[string]int{"5h": cap}}), "key", "subtest-cap-validation")).Want(http.StatusOK).JSON(&out)
		if out.Caps["5h"] != cap {
			t.Fatalf("cap=%d saved as %d", cap, out.Caps["5h"])
		}
	}
	// Custom providers may declare custom windows, but a cap must name one.
	createSubtestSubscription(t, h, map[string]any{"key": "subtest-custom-window", "provider": "claude", "windows": []string{"month"}, "caps": map[string]int{"month": 70}})
}

func TestSubscriptionReadsRedactCredentialsHostsAndLoginForNonAdmins(t *testing.T) {
	h := subscriptionTestHandler(t)
	h.SubscriptionLogins = NewSubscriptionLoginStore()
	sub := createSubtestSubscription(t, h, map[string]any{"key": "subtest-private-host", "provider": "claude", "credential_path": "/private/account/token"})
	dbfx.Insert(t, "subscription_host_state", testutil.Cols{"subscription_id": sub["id"], "daemon_id": "private-daemon", "device_name": "private-host", "credential_state": "present", "error": "/private/host/error"})
	h.SubscriptionLogins.start("subtest-private-host", "private-runtime", "")
	member := subtestOutsider(t)
	outsider := dbfx.Insert(t, "user", testutil.Cols{"name": "Other workspace", "email": "other-workspace-424@example.test"})
	workspace := dbfx.Insert(t, "workspace", testutil.Cols{"name": "Other", "slug": "private-subscription-424", "description": "", "issue_prefix": "OTH"})
	dbfx.Insert(t, "member", testutil.Cols{"workspace_id": workspace, "user_id": outsider, "role": "member"})
	for _, tc := range []struct {
		name, user, actor string
		manage            bool
	}{
		{"admin", testUserID, "", true},
		{"member", member, "", false},
		{"other-workspace", outsider, "", false},
		{"task-on-admin-account", testUserID, "task_token", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, list := range []bool{false, true} {
				req := withURLParam(newRequestAs(tc.user, "GET", "/", nil), "key", "subtest-private-host")
				req.Header.Set("X-Actor-Source", tc.actor)
				var out SubscriptionResponse
				if list {
					var result struct {
						Subscriptions []SubscriptionResponse `json:"subscriptions"`
						CanManage     bool                   `json:"can_manage"`
					}
					testutil.Call(t, h.ListSubscriptions, req).Want(http.StatusOK).JSON(&result)
					if result.CanManage != tc.manage {
						t.Fatalf("can_manage = %v", result.CanManage)
					}
					found := false
					for _, sub := range result.Subscriptions {
						if sub.Key == "subtest-private-host" {
							out, found = sub, true
						}
					}
					if !found {
						t.Fatal("account missing from overview")
					}
				} else {
					testutil.Call(t, h.GetSubscription, req).Want(http.StatusOK).JSON(&out)
				}
				if tc.manage {
					if out.CredentialPath != "/private/account/token" || len(out.Hosts) != 1 || out.Login == nil {
						t.Fatalf("admin cannot manage credential: %+v", out)
					}
				} else if out.CredentialPath != "" || len(out.Hosts) != 0 || out.Login != nil {
					t.Fatalf("non-admin received private credential metadata: %+v", out)
				}
				if out.LoginState != "logged_in" {
					t.Fatalf("public credential status lost: %s", out.LoginState)
				}
			}
		})
	}
}
