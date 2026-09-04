package deej

import "testing"

func TestButtonMapFromConfig(t *testing.T) {
	cases := []struct {
		name         string
		raw          interface{}
		wantCommands map[int]string
		wantWarnings int
	}{
		{
			name:         "nil config yields an empty map",
			raw:          nil,
			wantCommands: map[int]string{},
		},
		{
			// the whole reason this parser exists: viper's GetStringMapStringSlice
			// would have turned this into four separate "commands"
			name: "command with spaces stays a single command",
			raw: map[string]interface{}{
				"5": "playerctl -p spotify play-pause",
			},
			wantCommands: map[int]string{5: "playerctl -p spotify play-pause"},
		},
		{
			name: "surrounding whitespace is trimmed",
			raw: map[string]interface{}{
				"5": "  playerctl next  ",
			},
			wantCommands: map[int]string{5: "playerctl next"},
		},
		{
			name: "multiple buttons are all kept",
			raw: map[string]interface{}{
				"5": "playerctl -p spotify play-pause",
				"6": "playerctl -p spotify next",
			},
			wantCommands: map[int]string{
				5: "playerctl -p spotify play-pause",
				6: "playerctl -p spotify next",
			},
		},
		{
			name: "empty command is skipped with a warning",
			raw: map[string]interface{}{
				"5": "   ",
			},
			wantCommands: map[int]string{},
			wantWarnings: 1,
		},
		{
			name: "non-numeric index is skipped with a warning",
			raw: map[string]interface{}{
				"play": "playerctl play-pause",
			},
			wantCommands: map[int]string{},
			wantWarnings: 1,
		},
		{
			name: "negative index is skipped with a warning",
			raw: map[string]interface{}{
				"-1": "playerctl play-pause",
			},
			wantCommands: map[int]string{},
			wantWarnings: 1,
		},
		{
			// a YAML list is what you'd naturally try for "run two things";
			// we don't support it yet, so it must warn rather than misparse
			name: "list value is skipped with a warning",
			raw: map[string]interface{}{
				"5": []interface{}{"playerctl next", "playerctl play"},
			},
			wantCommands: map[int]string{},
			wantWarnings: 1,
		},
		{
			name:         "non-map config is rejected wholesale",
			raw:          "playerctl play-pause",
			wantCommands: map[int]string{},
			wantWarnings: 1,
		},
		{
			name: "good entries survive alongside bad ones",
			raw: map[string]interface{}{
				"5":    "playerctl -p spotify play-pause",
				"nope": "playerctl next",
			},
			wantCommands: map[int]string{5: "playerctl -p spotify play-pause"},
			wantWarnings: 1,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result, warnings := buttonMapFromConfig(testCase.raw)

			got := map[int]string{}
			result.iterate(func(idx int, command string) {
				got[idx] = command
			})

			if len(got) != len(testCase.wantCommands) {
				t.Fatalf("got %d mapped buttons (%v), want %d (%v)",
					len(got), got, len(testCase.wantCommands), testCase.wantCommands)
			}

			for idx, wantCommand := range testCase.wantCommands {
				if got[idx] != wantCommand {
					t.Errorf("button %d: got command %q, want %q", idx, got[idx], wantCommand)
				}
			}

			if len(warnings) != testCase.wantWarnings {
				t.Errorf("got %d warnings (%v), want %d", len(warnings), warnings, testCase.wantWarnings)
			}
		})
	}
}
