package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Ray0907/spanbox/internal/config"
)

var ErrSQLRejected = errors.New("sql rejected")

type SQLResult struct {
	Columns   []string
	Rows      [][]string
	Truncated bool
	Elapsed   time.Duration
}

var forbiddenSQL = map[string]bool{
	"ATTACH": true, "DETACH": true, "PRAGMA": true, "VACUUM": true, "REINDEX": true,
	"ALTER": true, "CREATE": true, "DROP": true, "INSERT": true, "UPDATE": true,
	"DELETE": true, "REPLACE": true, "BEGIN": true, "COMMIT": true, "ROLLBACK": true,
	"SAVEPOINT": true, "RELEASE": true,
}

var forbiddenFunctions = map[string]bool{
	"LOAD_EXTENSION": true, "READFILE": true, "WRITEFILE": true, "FTS5": true,
}

func CheckUserSQL(text string) error {
	if len(text) > config.SQLMaxTextBytes {
		return rejected("text exceeds 16 KiB")
	}
	tokens, err := sqlTokens(text)
	if err != nil {
		return rejected(err.Error())
	}
	if len(tokens) == 0 {
		return rejected("empty statement")
	}
	semicolons := 0
	for i, token := range tokens {
		if token == ";" {
			semicolons++
			if i != len(tokens)-1 || semicolons > 1 {
				return rejected("exactly one statement is allowed")
			}
		}
	}
	first := tokens[0]
	if first != "SELECT" && first != "WITH" && first != "EXPLAIN" {
		return rejected("statement must start with SELECT, WITH, or EXPLAIN")
	}
	for i, token := range tokens {
		if forbiddenSQL[token] {
			return rejected(strings.ToLower(token) + " is not allowed")
		}
		if forbiddenFunctions[token] && i+1 < len(tokens) && tokens[i+1] == "(" {
			return rejected(strings.ToLower(token) + "() is not allowed")
		}
	}
	return nil
}

func rejected(reason string) error { return fmt.Errorf("%w: %s", ErrSQLRejected, reason) }

func sqlTokens(text string) ([]string, error) {
	var tokens []string
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if unicode.IsSpace(r) {
			i += size
			continue
		}
		if strings.HasPrefix(text[i:], "--") {
			if end := strings.IndexByte(text[i+2:], '\n'); end >= 0 {
				i += end + 3
			} else {
				break
			}
			continue
		}
		if strings.HasPrefix(text[i:], "/*") {
			end := strings.Index(text[i+2:], "*/")
			if end < 0 {
				return nil, errors.New("unterminated comment")
			}
			i += end + 4
			continue
		}
		if text[i] == '\'' || text[i] == '"' || text[i] == '`' {
			next, ok := quotedEnd(text, i, text[i])
			if !ok {
				return nil, errors.New("unterminated quoted value")
			}
			i = next
			continue
		}
		if text[i] == '[' {
			next, ok := quotedEnd(text, i, ']')
			if !ok {
				return nil, errors.New("unterminated bracket identifier")
			}
			i = next
			continue
		}
		if isIdentifierStart(r) {
			start := i
			i += size
			for i < len(text) {
				r, size = utf8.DecodeRuneInString(text[i:])
				if !isIdentifierPart(r) {
					break
				}
				i += size
			}
			tokens = append(tokens, strings.ToUpper(text[start:i]))
			continue
		}
		tokens = append(tokens, text[i:i+size])
		i += size
	}
	return tokens, nil
}

func quotedEnd(text string, start int, end byte) (int, bool) {
	for i := start + 1; i < len(text); i++ {
		if text[i] != end {
			continue
		}
		if i+1 < len(text) && text[i+1] == end {
			i++
			continue
		}
		return i + 1, true
	}
	return 0, false
}

func isIdentifierStart(r rune) bool { return r == '_' || unicode.IsLetter(r) }
func isIdentifierPart(r rune) bool  { return isIdentifierStart(r) || unicode.IsDigit(r) }

func (s *Store) RunUserSQL(ctx context.Context, text string) (result SQLResult, err error) {
	started := time.Now()
	defer func() { result.Elapsed = time.Since(started) }()
	if err := CheckUserSQL(text); err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, config.SQLTimeout)
	defer cancel()
	conn, err := s.u.Conn(ctx)
	if err != nil {
		return result, err
	}
	defer conn.Close()
	rows, err := conn.QueryContext(ctx, text)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	result.Columns, err = rows.Columns()
	if err != nil {
		return result, err
	}
	if len(result.Columns) > config.SQLMaxCols {
		return result, fmt.Errorf("result has more than %d columns", config.SQLMaxCols)
	}
	totalBytes := 0
	for _, column := range result.Columns {
		totalBytes += len(column)
	}
	for rows.Next() {
		if len(result.Rows) == config.SQLMaxRows {
			result.Truncated = true
			break
		}
		values := make([]any, len(result.Columns))
		destinations := make([]any, len(values))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			return result, err
		}
		row := make([]string, len(values))
		rowBytes := 0
		for i, value := range values {
			row[i] = sqlString(value)
			if len(row[i]) > config.SQLMaxCellBytes {
				row[i] = truncateCell(row[i])
				result.Truncated = true
			}
			rowBytes += len(row[i])
		}
		if totalBytes+rowBytes > config.SQLMaxRespBytes {
			result.Truncated = true
			break
		}
		result.Rows = append(result.Rows, row)
		totalBytes += rowBytes
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	return result, nil
}

func sqlString(value any) string {
	switch value := value.(type) {
	case nil:
		return "NULL"
	case []byte:
		return string(value)
	case string:
		return value
	default:
		return fmt.Sprint(value)
	}
}

func truncateCell(value string) string {
	const marker = "...[truncated]"
	cut := config.SQLMaxCellBytes - len(marker)
	return strings.ToValidUTF8(value[:cut], "") + marker
}
