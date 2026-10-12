package mysqldump_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aliakseiz/go-mysqldump/v2"
	"github.com/stretchr/testify/require"
)

func TestSanitize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", input: "", want: ""},
		{name: "plain text", input: "test", want: "test"},
		{name: "NUL", input: "a\x00b", want: `a\0b`},
		{name: "single quote", input: "it's", want: `it\'s`},
		{name: "double quote", input: `say "hi"`, want: `say \"hi\"`},
		{name: "backspace", input: "a\bb", want: `a\bb`},
		{name: "newline", input: "a\nb", want: `a\nb`},
		{name: "carriage return", input: "a\rb", want: `a\rb`},
		{name: "ctrl-Z", input: "a\x1Ab", want: `a\Zb`},
		{name: "backslash", input: `a\b`, want: `a\\b`},
		{name: "backslash before quote", input: `\'`, want: `\\\'`},
		{name: "tab is not escaped", input: "a\tb", want: "a\tb"},
		{name: "percent and underscore are not escaped", input: "50%_off", want: "50%_off"},
		{name: "multibyte", input: "héllo привет 😀", want: "héllo привет 😀"},
		{name: "multibyte next to a quote", input: "п'р", want: `п\'р`},
		{name: "every special character", input: "\x00'\"\b\n\r\x1A\\", want: `\0\'\"\b\n\r\Z\\`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, mysqldump.Sanitize(tt.input))
		})
	}
}

func TestSanitize_QueryInjection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		format string
		input  string
		want   string
	}{
		{
			name:   "harmless value",
			format: "SELECT * WHERE field = '%s';",
			input:  "test",
			want:   "SELECT * WHERE field = 'test';",
		},
		{
			name:   "quote closing the literal",
			format: "'%s'",
			input:  "'; DROP TABLES `test`;",
			want:   "'\\'; DROP TABLES `test`;'",
		},
		{
			name:   "quote opening a concatenation",
			format: "'%s'",
			input:  "'+(SELECT name FROM users LIMIT 1)+'",
			want:   "'\\'+(SELECT name FROM users LIMIT 1)+\\''",
		},
		{
			name:   "NUL before a hex literal",
			format: "SELECT '%s'",
			input:  "\x00x633A5C626F6F742E696E69",
			want:   "SELECT '\\0x633A5C626F6F742E696E69'",
		},
		{
			name:   "tautology",
			format: "WHERE PASSWORD('%s')",
			input:  "') OR 1=1--",
			want:   "WHERE PASSWORD('\\') OR 1=1--')",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, fmt.Sprintf(tt.format, mysqldump.Sanitize(tt.input)))
		})
	}
}

// unescape reverses the MySQL string-literal escapes produced by Sanitize.
// It reports false when a raw special character or an unknown escape appears.
func unescape(escaped string) (string, bool) {
	replacements := map[byte]byte{'0': 0, '\'': '\'', '"': '"', 'b': '\b', 'n': '\n', 'r': '\r', 'Z': 0x1A, '\\': '\\'}

	var out strings.Builder

	for i := 0; i < len(escaped); i++ {
		char := escaped[i]

		switch char {
		case 0, '\'', '"', '\b', '\n', '\r', 0x1A:
			return "", false
		case '\\':
			i++

			if i == len(escaped) {
				return "", false
			}

			replacement, ok := replacements[escaped[i]]
			if !ok {
				return "", false
			}

			out.WriteByte(replacement)
		default:
			out.WriteByte(char)
		}
	}

	return out.String(), true
}

func FuzzSanitize(f *testing.F) {
	for _, seed := range []string{"", "plain", "it's", "\x00\\'\"\b\n\r\x1A", `\`, `\\'`, "привет", "a\tb"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		escaped := mysqldump.Sanitize(input)

		got, ok := unescape(escaped)
		require.True(t, ok, "escaped output %q holds a raw special character or an unknown escape", escaped)
		require.Equal(t, input, got, "escaping must be reversible")
	})
}

func ExampleSanitize() {
	fmt.Printf("SELECT '%s';\n", mysqldump.Sanitize("O'Brien\n"))
	// Output: SELECT 'O\'Brien\n';
}

func BenchmarkSanitize(b *testing.B) {
	inputs := map[string]string{
		"clean 64B":    strings.Repeat("a", 64),
		"clean 4KiB":   strings.Repeat("a", 4096),
		"special 64B":  strings.Repeat("a'", 32),
		"special 4KiB": strings.Repeat("a'", 2048),
	}

	for name, input := range inputs {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(input)))

			for b.Loop() {
				_ = mysqldump.Sanitize(input)
			}
		})
	}
}
