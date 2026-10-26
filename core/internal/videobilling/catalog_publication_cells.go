package videobilling

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"unicode/utf8"
)

// 0111 has five 64-bit integers (20 bytes each) and two VARCHAR(16)
// utf8mb4 strings (at most 16*12 escaped bytes plus quotes each). Including
// normalized separators takes at most 504 bytes per tuple; round to 512,
// then include the outer brackets. This is an acceptance limit, not a bound
// on driver allocations or total heap. See the publication contract §14.4.
const maxCatalogMinimumJSONBytes = 512*32001 + 2

type catalogMinimumAggregate struct {
	CellCount    sql.NullInt64
	CellsJSON    []byte
	DecodedCount int
}

type catalogMinimumRows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

// The caller retains the SQL child context through Close. Count physical
// rows actually visited, including an unexpected second row or failed Scan.
func scanCatalogMinimumAggregate(ctx context.Context, rows catalogMinimumRows, result *catalogMinimumAggregate) (count int64, err error) {
	defer func() {
		if closeErr := rows.Close(); err == nil {
			err = closeErr
		}
		if contextErr := ctx.Err(); contextErr != nil {
			err = contextErr
		}
	}()
	if err = ctx.Err(); err != nil {
		return 0, err
	}
	if !rows.Next() {
		if err = rows.Err(); err == nil {
			err = ErrCatalogTariffUnavailable
		}
		return 0, err
	}
	count = 1
	if err = rows.Scan(&result.CellCount, &result.CellsJSON); err != nil {
		return count, err
	}
	if err = ctx.Err(); err != nil {
		return count, err
	}
	if rows.Next() {
		return 2, ErrCatalogTariffUnavailable
	}
	return count, rows.Err()
}

func decodeCatalogMinimumAggregate(ctx context.Context, result *catalogMinimumAggregate) (cells []MinimumRuleCell, err error) {
	// Cancellation may happen inside the JSON library or integer/string
	// validation. Preserve the parent cause even when the same input is bad;
	// neither the error path nor a late cancellation may return partial cells.
	defer func() {
		if contextErr := ctx.Err(); contextErr != nil {
			cells, err = nil, contextErr
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !result.CellCount.Valid || result.CellCount.Int64 < 0 || result.CellCount.Int64 > 32001 {
		return nil, ErrCatalogTariffUnavailable
	}
	if len(result.CellsJSON) > maxCatalogMinimumJSONBytes {
		return nil, ErrCatalogTariffLimit
	}
	if result.CellCount.Int64 > 32000 {
		return nil, ErrCatalogTariffLimit
	}
	if result.CellsJSON == nil {
		if result.CellCount.Int64 == 0 { // MySQL's explicit empty aggregate.
			return nil, nil
		}
		return nil, ErrCatalogTariffUnavailable
	}
	if !utf8.Valid(result.CellsJSON) {
		return nil, ErrCatalogTariffUnavailable
	}
	cursor := catalogMinimumCursor{raw: result.CellsJSON, ctx: ctx}
	if !cursor.take('[') || !cursor.space() {
		return nil, ErrCatalogTariffUnavailable
	}
	cells = make([]MinimumRuleCell, 0, int(result.CellCount.Int64))
	for cursor.pos < len(cursor.raw) && cursor.raw[cursor.pos] != ']' {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(cells) >= int(result.CellCount.Int64) {
			return nil, ErrCatalogTariffUnavailable
		}
		// Only this fixed seven-scalar grammar is accepted. Fields borrow the
		// original bytes; no reflective decode or per-field raw copy is needed.
		fields, ok := cursor.tuple()
		if !ok {
			return nil, ErrCatalogTariffUnavailable
		}
		cell, ok := decodeCatalogMinimumTuple(fields)
		if !ok {
			return nil, ErrCatalogTariffUnavailable
		}
		cells = append(cells, cell)
		result.DecodedCount++
		if !cursor.space() || cursor.pos == len(cursor.raw) {
			return nil, ErrCatalogTariffUnavailable
		}
		if cursor.raw[cursor.pos] == ']' {
			break
		}
		if !cursor.take(',') || !cursor.space() || cursor.pos == len(cursor.raw) || cursor.raw[cursor.pos] != '[' {
			return nil, ErrCatalogTariffUnavailable
		}
	}
	if !cursor.take(']') || len(cells) != int(result.CellCount.Int64) {
		return nil, ErrCatalogTariffUnavailable
	}
	if !cursor.space() || cursor.pos != len(cursor.raw) {
		return nil, ErrCatalogTariffUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return cells, nil
}

// This is a schema-specific cursor, not a permissive JSON parser. It accepts
// arrays of exactly seven fields, with strings only at positions two and three.
// Semantic integer and Unicode checks remain in decodeCatalogMinimumTuple.
type catalogMinimumCursor struct {
	raw []byte
	pos int
	ctx context.Context
}

func (c *catalogMinimumCursor) space() bool {
	for c.pos < len(c.raw) {
		switch c.raw[c.pos] {
		case ' ', '\t', '\r', '\n':
			c.pos++
			// Absolute offsets bound cancellation checks across calls as well as
			// a single long whitespace run. No Unicode whitespace is JSON space.
			if c.pos&4095 == 0 && c.ctx.Err() != nil {
				return false
			}
		default:
			return true
		}
	}
	return true
}

func (c *catalogMinimumCursor) take(want byte) bool {
	if !c.space() || c.pos == len(c.raw) || c.raw[c.pos] != want {
		return false
	}
	c.pos++
	return true
}

func (c *catalogMinimumCursor) tuple() (fields [7]json.RawMessage, ok bool) {
	if !c.take('[') {
		return fields, false
	}
	for i := range fields {
		if i == 2 || i == 3 {
			fields[i], ok = c.stringField()
		} else {
			fields[i], ok = c.integerField()
		}
		if !ok {
			return fields, false
		}
		separator := byte(',')
		if i == len(fields)-1 {
			separator = ']'
		}
		if !c.take(separator) {
			return fields, false
		}
	}
	return fields, true
}

func (c *catalogMinimumCursor) integerField() (json.RawMessage, bool) {
	if !c.space() || c.pos == len(c.raw) {
		return nil, false
	}
	start := c.pos
	if c.raw[c.pos] == '-' {
		c.pos++
	}
	if c.pos == len(c.raw) || c.raw[c.pos] < '0' || c.raw[c.pos] > '9' {
		return nil, false
	}
	zero := c.raw[c.pos] == '0'
	c.pos++
	for c.pos < len(c.raw) && c.raw[c.pos] >= '0' && c.raw[c.pos] <= '9' {
		if zero || c.pos-start >= 20 {
			return nil, false
		}
		c.pos++
	}
	// The tuple then requires comma or closing bracket after JSON whitespace;
	// therefore exponents, fractions and token suffixes cannot be accepted.
	// ParseUint rejects any negative unsigned value, including -0; ParseInt
	// retains the prior legal signed -0 behavior and rejects overflow.
	return c.raw[start:c.pos], true
}

func (c *catalogMinimumCursor) stringField() (json.RawMessage, bool) {
	if !c.space() || c.pos == len(c.raw) || c.raw[c.pos] != '"' {
		return nil, false
	}
	start := c.pos
	c.pos++
	for c.pos < len(c.raw) && c.pos-start < 194 {
		switch b := c.raw[c.pos]; {
		case b == '"':
			c.pos++
			return c.raw[start:c.pos], true
		case b < 0x20:
			return nil, false
		case b == '\\':
			if c.pos+1 >= len(c.raw) {
				return nil, false
			}
			switch c.raw[c.pos+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				c.pos += 2
			case 'u':
				if c.pos+6 > len(c.raw) {
					return nil, false
				}
				for _, h := range c.raw[c.pos+2 : c.pos+6] {
					if !(h >= '0' && h <= '9' || h >= 'a' && h <= 'f' || h >= 'A' && h <= 'F') {
						return nil, false
					}
				}
				c.pos += 6
			default:
				return nil, false
			}
		default:
			c.pos++
		}
	}
	return nil, false
}

func decodeCatalogMinimumTuple(fields [7]json.RawMessage) (MinimumRuleCell, bool) {
	var cell MinimumRuleCell
	var err error
	if cell.ID, err = strconv.ParseUint(string(fields[0]), 10, 64); err != nil {
		return cell, false
	}
	if cell.RuleSetID, err = strconv.ParseUint(string(fields[1]), 10, 64); err != nil {
		return cell, false
	}
	if cell.Resolution, err = catalogMinimumJSONString(fields[2]); err != nil {
		return cell, false
	}
	if cell.Ratio, err = catalogMinimumJSONString(fields[3]); err != nil {
		return cell, false
	}
	if cell.OutputDurationMS, err = strconv.ParseInt(string(fields[4]), 10, 64); err != nil {
		return cell, false
	}
	if cell.FPS, err = strconv.ParseInt(string(fields[5]), 10, 64); err != nil {
		return cell, false
	}
	if cell.MinimumTokens, err = strconv.ParseInt(string(fields[6]), 10, 64); err != nil {
		return cell, false
	}
	return cell, true
}

func catalogMinimumJSONString(raw []byte) (string, error) {
	if len(raw) < 2 || len(raw) > 194 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return "", ErrCatalogTariffUnavailable
	}
	// encoding/json otherwise replaces unpaired UTF-16 escapes with U+FFFD.
	// Reject them instead of silently changing an immutable database fact.
	for i := 1; i < len(raw)-1; i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return "", ErrCatalogTariffUnavailable
		}
		u, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil || (u >= 0xdc00 && u <= 0xdfff) {
			return "", ErrCatalogTariffUnavailable
		}
		i += 4
		if u >= 0xd800 && u <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return "", ErrCatalogTariffUnavailable
			}
			low, e := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if e != nil || low < 0xdc00 || low > 0xdfff {
				return "", ErrCatalogTariffUnavailable
			}
			i += 6
		}
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || utf8.RuneCountInString(value) > 16 {
		return "", ErrCatalogTariffUnavailable
	}
	return value, nil
}
