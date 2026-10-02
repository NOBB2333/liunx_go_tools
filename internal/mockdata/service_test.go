package mockdata

import (
	"context"
	"strings"
	"testing"
)

func TestRunRejectsInvalidConfigurationBeforeConnecting(t *testing.T) {
	tests := []MockDataConfig{
		{DSN: "", Tables: []string{"users"}, Rows: 1},
		{DSN: "dsn", Tables: []string{"users"}, Rows: 0},
		{DSN: "dsn", Tables: []string{"users; DROP TABLE users"}, Rows: 1},
	}
	for _, test := range tests {
		err := NewMockDataService().Run(context.Background(), test)
		if err == nil {
			t.Fatalf("expected validation error for %+v", test)
		}
		if strings.Contains(strings.ToLower(err.Error()), "connect") {
			t.Fatalf("configuration should fail before connecting: %v", err)
		}
	}
}
