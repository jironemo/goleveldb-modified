package leveldb

import (
	"errors"
	"fmt"
	"strings"
)

type JoinTuple struct {
	OuterKey   []byte
	OuterValue []byte
	InnerKey   []byte
	InnerValue []byte
}

type ASTNode interface {
	Eval(dbs map[string]*DB) ([]JoinTuple, error)
}

type JoinAST struct {
	TableA string
	TableB string
}

func (ast *JoinAST) Eval(dbs map[string]*DB) ([]JoinTuple, error) {
	dbA, okA := dbs[ast.TableA]
	dbB, okB := dbs[ast.TableB]
	if !okA || !okB {
		return nil, fmt.Errorf("tables not found: %s, %s", ast.TableA, ast.TableB)
	}

	// Designate the smaller table as the outer loop and the larger table as the inner loop.
	var outerDB, innerDB *DB
	if dbA.NumEntries() <= dbB.NumEntries() {
		outerDB = dbA
		innerDB = dbB
	} else {
		outerDB = dbB
		innerDB = dbA
	}

	// Stream records from the outer table using B-tree RangeQuery (nil boundaries streams all keys).
	outerKeys, outerValues, err := outerDB.RangeQuery(nil, nil)
	if err != nil {
		return nil, err
	}

	var results []JoinTuple
	for i := 0; i < len(outerKeys); i++ {
		oKey := outerKeys[i]
		oVal := outerValues[i]

		// The foreign key is streamed from the outer table (using the value as the FK).
		foreignKey := oVal

		// The AI Shield: Probe the inner table's Learned Existence Index and Overflow Bloom filter.
		if !innerDB.checkExistenceInSSTables(foreignKey) {
			// Instantly drop the tuple and advance the outer loop without touching disk.
			continue
		}

		// Retrieve matching inner record using B-tree + direct disk read.
		iVal, err := innerDB.Get(foreignKey, nil)
		if err == nil {
			results = append(results, JoinTuple{
				OuterKey:   oKey,
				OuterValue: oVal,
				InnerKey:   foreignKey,
				InnerValue: iVal,
			})
		}
	}

	return results, nil
}

// ParseJoin parses a join query command string like "db.join('table_a', 'table_b')" or "join(table_a, table_b)"
func ParseJoin(cmd string) (ASTNode, error) {
	cmd = strings.TrimSpace(cmd)
	var content string

	if strings.HasPrefix(cmd, "db.join(") && strings.HasSuffix(cmd, ")") {
		content = cmd[len("db.join(") : len(cmd)-1]
	} else if strings.HasPrefix(cmd, "join(") && strings.HasSuffix(cmd, ")") {
		content = cmd[len("join(") : len(cmd)-1]
	} else {
		return nil, errors.New("invalid join syntax: must be join(table_a, table_b) or db.join(table_a, table_b)")
	}

	parts := strings.Split(content, ",")
	if len(parts) != 2 {
		return nil, errors.New("join requires exactly two table arguments")
	}

	tableA := strings.Trim(strings.TrimSpace(parts[0]), "'\"")
	tableB := strings.Trim(strings.TrimSpace(parts[1]), "'\"")

	if tableA == "" || tableB == "" {
		return nil, errors.New("table names cannot be empty")
	}

	return &JoinAST{
		TableA: tableA,
		TableB: tableB,
	}, nil
}
