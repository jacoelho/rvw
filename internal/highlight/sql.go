package highlight

import "strings"

// SQL, across the common dialects. Words ignore case. A quoted name ("x", `x`) is a name, not a
// string; a string doubles its quote to hold one, and an E'' string also takes backslash
// escapes. PostgreSQL's dollar-quoted strings, -- and /* */ comments, and $1, :name and @name
// parameters are read. A name after a dot is a column or table, never a keyword.

var (
	sqlKeywords = words(`ADD ALL ALTER ANALYZE AND ANY AS ASC AUTOINCREMENT AUTO_INCREMENT BEGIN
		BETWEEN BY CASCADE CASE CHECK COLLATE COLUMN COMMIT CONFLICT CONSTRAINT CREATE CROSS CURRENT
		DATABASE DECLARE DEFAULT DELETE DESC DISTINCT DO DROP EACH ELSE END ESCAPE EXCEPT EXECUTE
		EXISTS EXPLAIN FALSE FETCH FILTER FIRST FOLLOWING FOR FOREIGN FROM FULL FUNCTION GRANT GROUP
		HAVING IF ILIKE IN INDEX INNER INSERT INTERSECT INTO IS JOIN KEY LANGUAGE LATERAL LEFT LIKE
		LIMIT MATERIALIZED MERGE MATCHED NATURAL NEXT NOT NOTHING NULL NULLS OF OFFSET ON ONLY OR
		ORDER OUTER OVER PARTITION PRECEDING PRIMARY PROCEDURE RANGE RECURSIVE REFERENCES RENAME
		REPLACE RESTRICT RETURN RETURNING RETURNS REVOKE RIGHT ROLLBACK ROW ROWS SAVEPOINT SCHEMA
		SELECT SEQUENCE SET SOME TABLE TEMP TEMPORARY THEN TO TRANSACTION TRIGGER TRUE TRUNCATE
		UNBOUNDED UNION UNIQUE UPDATE USING VALUES VIEW WHEN WHERE WINDOW WITH WITHOUT`)
	sqlTypes = words(`ARRAY BIGINT BIGSERIAL BINARY BIT BLOB BOOL BOOLEAN BYTEA CHAR CHARACTER CIDR
		CLOB DATE DATETIME DECIMAL DOUBLE FLOAT FLOAT4 FLOAT8 INET INT INT2 INT4 INT8 INTEGER
		INTERVAL JSON JSONB MEDIUMINT MONEY NCHAR NUMERIC NVARCHAR PRECISION REAL SERIAL SMALLINT
		SMALLSERIAL TEXT TIME TIMESTAMP TIMESTAMPTZ TINYINT UUID VARBINARY VARCHAR VARYING XML ZONE`)
)

func scanSQL(o *out, s string, i int) {
	for i < len(s) && !o.stopped() {
		c := s[i]
		switch {
		case c == '-' && nextByte(s, i) == '-':
			e := lineEnd(s, i)
			o.tok(i, e, ClassComment)
			i = e
		case c == '/' && nextByte(s, i) == '*':
			e := closeEnd(s, i+2, "*/")
			o.tok(i, e, ClassComment)
			i = e
		case c == '\'':
			e := sqlQuoteEnd(s, i, false)
			o.tok(i, e, ClassString)
			i = e
		case c == '"' || c == '`':
			i = sqlQuoteEnd(s, i, false) // a quoted name
		case c == '$':
			i = sqlDollar(o, s, i)
		case (c == ':' || c == '@') && i+1 < len(s) && (isIdentStart(s[i+1]) || s[i+1] == '@'):
			if c == ':' && i > 0 && s[i-1] == ':' { // the second colon of a :: cast
				o.tok(i, i+1, ClassOperator)
				i++
				continue
			}
			e := identEnd(s, i+2)
			o.tok(i, e, ClassType)
			i = e
		case isDigit(c) || c == '.' && isDigit(nextByte(s, i)):
			e := numberEnd(s, i)
			o.tok(i, e, ClassNumber)
			i = e
		case isIdentStart(c):
			i = sqlWord(o, s, i)
		case cOperators[c] || c == ':':
			o.tok(i, i+1, ClassOperator)
			i++
		default:
			i++
		}
	}
}

// sqlWord classes the word at i, or the string it prefixes (E”, N”, B”, X”), and returns its
// end. A name after a dot is a column, table or function, never a keyword or type.
func sqlWord(o *out, s string, i int) int {
	e := identEnd(s, i)
	w := s[i:e]
	if e-i == 1 && e < len(s) && s[e] == '\'' && strings.IndexByte("EeNnBbXx", w[0]) >= 0 {
		end := sqlQuoteEnd(s, e, w[0]|0x20 == 'e')
		o.tok(i, end, ClassString)
		return end
	}
	qualified := i > 0 && s[i-1] == '.'
	class := ""
	switch {
	case !qualified && lookupFold(sqlKeywords, w):
		class = ClassKeyword
	case !qualified && lookupFold(sqlTypes, w):
		class = ClassType
	case nextIs(s, e, '('):
		class = ClassFunction
	}
	o.tok(i, e, class)
	return e
}

// sqlQuoteEnd returns the index after the quote that closes the one at i, where a doubled quote
// is part of the text and, with backslash, a backslash escapes the next byte. A string may span
// lines; left open, it ends at the end of s.
func sqlQuoteEnd(s string, i int, backslash bool) int {
	q := s[i]
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			if backslash {
				j++
			}
		case q:
			if j+1 < len(s) && s[j+1] == q {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(s)
}

// sqlDollar classes the $1 parameter or $tag$ string at i and returns its end.
func sqlDollar(o *out, s string, i int) int {
	e := i + 1
	if e < len(s) && isDigit(s[e]) {
		e = digitsEnd(s, e)
		o.tok(i, e, ClassType)
		return e
	}
	for e < len(s) && isASCIIIdentByte(s[e]) {
		e++
	}
	if e >= len(s) || s[e] != '$' {
		return e
	}
	tag := s[i : e+1]
	end := closeEnd(s, e+1, tag)
	o.tok(i, end, ClassString)
	return end
}
