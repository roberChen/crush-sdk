package imwrap

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prefix  string
		text    string
		kind    Kind
		cmdName string
		args    []string
		argText string
	}{
		{"empty", "/", "", KindIgnore, "", nil, ""},
		{"whitespace", "/", "   \n\t ", KindIgnore, "", nil, ""},
		{"lone prefix", "/", "/", KindIgnore, "", nil, ""},
		{"prefix and space", "/", "/ hi", KindIgnore, "", nil, ""},
		{"command", "/", "/new my title", KindCommand, "new", []string{"my", "title"}, "my title"},
		{"command case", "/", "/SWITCH 2", KindCommand, "switch", []string{"2"}, "2"},
		{"command no args", "/", "/sessions", KindCommand, "sessions", []string{}, ""},
		{"command extra spaces", "/", "/export   a  b", KindCommand, "export", []string{"a", "b"}, "a  b"},
		{"custom prefix", "!", "!deploy prod", KindCommand, "deploy", []string{"prod"}, "prod"},
		{"agent text", "/", "hello world", KindAgent, "", nil, ""},
		{"agent slash inside", "/", "see /etc/hosts file", KindAgent, "", nil, ""},
		{"agent multiline", "/", "line1\nline2", KindAgent, "", nil, ""},
		{"agent keeps raw", "/", "  padded  ", KindAgent, "", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Parse(tt.prefix, tt.text)
			require.Equal(t, tt.kind, got.Kind)
			require.Equal(t, tt.cmdName, got.Name)
			require.Equal(t, tt.args, got.Args)
			require.Equal(t, tt.argText, got.ArgText)
		})
	}
}

func TestKindString(t *testing.T) {
	t.Parallel()
	require.Equal(t, "command", KindCommand.String())
	require.Equal(t, "agent", KindAgent.String())
	require.Equal(t, "ignore", KindIgnore.String())
}
