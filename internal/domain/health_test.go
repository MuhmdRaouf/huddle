package domain

import "testing"

const healthT0 int64 = 1_700_000_000_000

func TestHealthStateOf(t *testing.T) {
	tests := []struct {
		name string
		part HealthComponent
		now  int64
		want HealthState
	}{
		{
			name: "an error after the last success is failing even when the loop is also late",
			part: HealthComponent{Name: "sweep", Kind: HealthLoop, EveryMs: 1_000, LastOKAt: healthT0, LastErrAt: healthT0 + 5_000},
			now:  healthT0 + 120_000,
			want: HealthFailing,
		},
		{
			name: "an error with no success yet is failing",
			part: HealthComponent{Name: "hub.db", Kind: HealthStore, LastErrAt: healthT0, LastError: "locked"},
			now:  healthT0 + 1,
			want: HealthFailing,
		},
		{
			name: "an error at the same moment as the success is not failing",
			part: HealthComponent{Name: "hub.db", Kind: HealthStore, LastOKAt: healthT0, LastErrAt: healthT0},
			now:  healthT0,
			want: HealthOK,
		},
		{
			name: "a loop late by exactly three periods is ok",
			part: HealthComponent{Name: "sweep", Kind: HealthLoop, EveryMs: 1_000, LastOKAt: healthT0},
			now:  healthT0 + 3_000,
			want: HealthOK,
		},
		{
			name: "a loop one ms past three periods is stale",
			part: HealthComponent{Name: "sweep", Kind: HealthLoop, EveryMs: 1_000, LastOKAt: healthT0},
			now:  healthT0 + 3_001,
			want: HealthStale,
		},
		{
			name: "a store is never stale",
			part: HealthComponent{Name: "hub.db", Kind: HealthStore, LastOKAt: healthT0},
			now:  healthT0 + 10_000_000,
			want: HealthOK,
		},
		{
			name: "a loop with no period is never stale",
			part: HealthComponent{Name: "sweep", Kind: HealthLoop, LastOKAt: healthT0},
			now:  healthT0 + 10_000_000,
			want: HealthOK,
		},
		{
			name: "a store never reported is unknown",
			part: HealthComponent{Name: "hub.db", Kind: HealthStore},
			now:  healthT0 + 10_000_000,
			want: HealthUnknown,
		},
		{
			name: "a loop never reported is unknown",
			part: HealthComponent{Name: "sweep", Kind: HealthLoop, EveryMs: 1_000},
			now:  healthT0 + 10_000_000,
			want: HealthUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HealthStateOf(tt.part, tt.now); got != tt.want {
				t.Errorf("HealthStateOf(%+v, %d) = %q, want %q", tt.part, tt.now, got, tt.want)
			}
		})
	}
}
