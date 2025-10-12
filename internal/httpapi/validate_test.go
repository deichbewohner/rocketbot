package httpapi

import (
	"strings"
	"testing"
)

func TestValidateTarget(t *testing.T) {
	tests := []struct {
		name    string
		target  Target
		wantErr bool
		errMsg  string
	}{
		{name: "valid_username", target: Target{Username: "alice"}},
		{name: "valid_channel", target: Target{Channel: "general"}},
		{name: "valid_roomId", target: Target{RoomID: "abc123"}},
		{
			name:    "empty_target",
			target:  Target{},
			wantErr: true,
			errMsg:  "must specify username, channel, or roomId",
		},
		{
			name:    "username_and_channel",
			target:  Target{Username: "alice", Channel: "general"},
			wantErr: true,
			errMsg:  "only one",
		},
		{
			name:    "username_and_roomId",
			target:  Target{Username: "alice", RoomID: "abc123"},
			wantErr: true,
			errMsg:  "only one",
		},
		{
			name:    "channel_and_roomId",
			target:  Target{Channel: "general", RoomID: "abc123"},
			wantErr: true,
			errMsg:  "only one",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTarget(tt.target)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateTarget() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && err != nil && !strings.Contains(err.Error(), tt.errMsg) {
				t.Errorf("error = %q, want to contain %q", err.Error(), tt.errMsg)
			}
		})
	}
}
