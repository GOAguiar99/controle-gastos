package app

import (
	"testing"
	"time"
)

// Last use is recorded on web and MCP requests, at most every lastSeenEvery
// per user (and per MCP link), and the actor carries the previous value.
func TestLastSeenThrottle(t *testing.T) {
	a, d, now := newApp(t)
	t0 := *now
	act, err := a.SignIn(ctx, ana())
	if err != nil {
		t.Fatal(err)
	}
	seen := func() time.Time {
		t.Helper()
		u, err := d.User(ctx, act.User.ID)
		if err != nil {
			t.Fatal(err)
		}
		return u.LastSeenAt
	}
	if !seen().IsZero() {
		t.Fatal("a new account starts as seen")
	}
	tok, err := a.StartSession(ctx, act.User.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	resume := func(at time.Time) Actor {
		t.Helper()
		*now = at
		got, ok, err := a.ResumeSession(ctx, tok)
		if err != nil || !ok {
			t.Fatalf("resume at %v: %v %v", at, ok, err)
		}
		return got
	}

	if got := resume(t0); !got.User.LastSeenAt.IsZero() {
		t.Errorf("first request: actor should carry the previous (never) value, got %v", got.User.LastSeenAt)
	}
	if !seen().Equal(t0) {
		t.Fatalf("first request not recorded: %v", seen())
	}
	if got := resume(t0.Add(2 * time.Minute)); !got.User.LastSeenAt.Equal(t0) || !seen().Equal(t0) {
		t.Fatalf("within the throttle: actor %v, db %v", got.User.LastSeenAt, seen())
	}
	t1 := t0.Add(6 * time.Minute)
	resume(t1)
	if !seen().Equal(t1) {
		t.Fatalf("after the throttle: %v", seen())
	}

	// an MCP link counts as use too, and records its own last use
	*now = t1
	link, err := a.CreateMCPToken(ctx, act, "")
	if err != nil {
		t.Fatal(err)
	}
	linkUsed := func() time.Time {
		t.Helper()
		ts, err := a.MCPTokens(ctx, act)
		if err != nil || len(ts) != 1 {
			t.Fatalf("links: %v %v", ts, err)
		}
		return ts[0].LastUsedAt
	}
	mcp := func(at time.Time) {
		t.Helper()
		*now = at
		if _, ok, err := a.MCPActor(ctx, link); err != nil || !ok {
			t.Fatalf("mcp at %v: %v %v", at, ok, err)
		}
	}
	t2 := t1.Add(time.Minute)
	mcp(t2)
	if !linkUsed().Equal(t2) || !seen().Equal(t1) {
		t.Fatalf("first MCP call: link %v, user %v", linkUsed(), seen())
	}
	mcp(t2.Add(time.Minute))
	if !linkUsed().Equal(t2) {
		t.Fatalf("MCP link written within the throttle: %v", linkUsed())
	}
	t3 := t2.Add(10 * time.Minute)
	mcp(t3)
	if !linkUsed().Equal(t3) || !seen().Equal(t3) {
		t.Fatalf("MCP after the throttle: link %v, user %v", linkUsed(), seen())
	}

	// the clock going back never moves it backwards
	resume(t3.Add(-time.Hour))
	if !seen().Equal(t3) {
		t.Fatalf("moved backwards: %v", seen())
	}

	// members of the ledger see it
	ms, err := d.Members(ctx, act.LedgerID)
	if err != nil || len(ms) != 1 || !ms[0].LastSeenAt.Equal(t3) {
		t.Fatalf("members: %+v %v", ms, err)
	}
}
