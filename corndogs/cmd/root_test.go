package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestHelpDescribesCurrentCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		args []string
		want []string
	}{
		{args: []string{"--help"}, want: []string{"Usage:\n  corndogs", "run", "submit-task", "timeout"}},
		{args: []string{"submit-task", "--help"}, want: []string{"Submit a task", "Timeout in seconds", "higher values are claimed first"}},
		{args: []string{"timeout", "--help"}, want: []string{"Process tasks that are timed out", "all queues if empty"}},
		{args: []string{"run", "--help"}, want: []string{"Run the Corndogs service"}},
	}

	for _, test := range tests {
		test := test
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			cmd := NewRootCommand()
			cmd.SetArgs(test.args)
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			for _, want := range test.want {
				if !strings.Contains(output.String(), want) {
					t.Errorf("help does not contain %q:\n%s", want, output.String())
				}
			}
		})
	}
}

func TestCommandsRejectArguments(t *testing.T) {
	t.Parallel()

	cmd := NewRootCommand()
	cmd.SetArgs([]string{"submit-task", "unexpected"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want an argument error")
	}
}
