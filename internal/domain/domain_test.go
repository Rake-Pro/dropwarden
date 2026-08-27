package domain

import "testing"

func TestRequiresAccountLink(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name      string
		url       string
		connected *bool
		want      bool
	}{
		{"unknown signal, no url", "", nil, false},
		{"unknown signal, url present", "https://x", nil, true},
		{"connected overrides url", "https://x", &yes, false},
		{"no link needed reports connected", "", &yes, false},
		{"unlinked requires link even without url", "", &no, true},
		{"unlinked with url", "https://x", &no, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Campaign{AccountLinkURL: tc.url, AccountConnected: tc.connected}
			if got := c.RequiresAccountLink(); got != tc.want {
				t.Fatalf("RequiresAccountLink()=%v want %v", got, tc.want)
			}
		})
	}
}

func TestProgressEffective(t *testing.T) {
	cases := []struct {
		name     string
		p        Progress
		wantMin  int
		wantAuth bool
		wantDone bool
	}{
		{
			name:    "server confirmed",
			p:       Progress{RealMinutes: 40, ExtraMinutes: 5, RequiredMinutes: 60, Source: SourceInventory},
			wantMin: 40, wantAuth: true, wantDone: false,
		},
		{
			name:    "estimate adds capped extra",
			p:       Progress{RealMinutes: 40, ExtraMinutes: 30, RequiredMinutes: 60, Source: SourceEstimate},
			wantMin: 55, wantAuth: false, wantDone: false, // 40 + min(30,15)
		},
		{
			name:    "complete by estimate",
			p:       Progress{RealMinutes: 50, ExtraMinutes: 15, RequiredMinutes: 60, Source: SourceWebSocket},
			wantMin: 65, wantAuth: false, wantDone: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, auth := tc.p.Effective()
			if m != tc.wantMin || auth != tc.wantAuth {
				t.Fatalf("Effective()=%d,%v want %d,%v", m, auth, tc.wantMin, tc.wantAuth)
			}
			if got := tc.p.Complete(); got != tc.wantDone {
				t.Fatalf("Complete()=%v want %v", got, tc.wantDone)
			}
		})
	}
}

func TestCampaignAnyChannel(t *testing.T) {
	if !(Campaign{}).AnyChannel() {
		t.Fatal("empty ACL should mean any channel")
	}
	c := Campaign{AllowedChannels: []Channel{{Login: "shroud"}}}
	if c.AnyChannel() {
		t.Fatal("non-empty ACL should not be any-channel")
	}
}

func TestSessionMinutesWatched(t *testing.T) {
	if got := (WatchSession{StartMinutes: 10, EndMinutes: 25}).MinutesWatched(); got != 15 {
		t.Fatalf("MinutesWatched=%d want 15", got)
	}
	if got := (WatchSession{StartMinutes: 25, EndMinutes: 10}).MinutesWatched(); got != 0 {
		t.Fatalf("negative should clamp to 0, got %d", got)
	}
}

func TestValidPriorityMode(t *testing.T) {
	if !ValidPriorityMode(PriorityTopDown) || ValidPriorityMode("bogus") {
		t.Fatal("priority mode validation wrong")
	}
}
