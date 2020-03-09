package search

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func Test_ScanParameter(t *testing.T) {
	cases := []struct {
		Name  string
		Input string
		Want  string
	}{
		{
			Name:  "Normal field:value",
			Input: `file:README.md`,
			Want:  `{"field":"file","value":"README.md","negated":false}`,
		},

		{
			Name:  "First char is colon",
			Input: `:foo`,
			Want:  `{"field":"","value":":foo","negated":false}`,
		},
		{
			Name:  "Last char is colon",
			Input: `foo:`,
			Want:  `{"field":"foo","value":"","negated":false}`,
		},
		{
			Name:  "Match first colon",
			Input: `foo:bar:baz`,
			Want:  `{"field":"foo","value":"bar:baz","negated":false}`,
		},
		{
			Name:  "No field, start with minus",
			Input: `-:foo`,
			Want:  `{"field":"","value":"-:foo","negated":false}`,
		},
		{
			Name:  "Minus prefix on field",
			Input: `-file:README.md`,
			Want:  `{"field":"file","value":"README.md","negated":true}`,
		},
		{
			Name:  "Double minus prefix on field",
			Input: `--foo:bar`,
			Want:  `{"field":"","value":"--foo:bar","negated":false}`,
		},
		{
			Name:  "Minus in the middle is not a valid field",
			Input: `fie-ld:bar`,
			Want:  `{"field":"","value":"fie-ld:bar","negated":false}`,
		},
		{
			Name:  "No effect on escaped whitespace",
			Input: `a\ pattern`,
			Want:  `{"field":"","value":"a\\ pattern","negated":false}`,
		},
	}
	for _, tt := range cases {
		t.Run(tt.Name, func(t *testing.T) {
			parser := &parser{buf: []byte(tt.Input)}
			result := parser.ParseParameter()
			got, _ := json.Marshal(result)
			if diff := cmp.Diff(tt.Want, string(got)); diff != "" {
				t.Error(diff)
			}
		})
	}
}

func Test_Parse(t *testing.T) {
	cases := []struct {
		Name  string
		Input string
		Want  string
	}{
		/*
			{
				Name:  "Empty string",
				Input: "",
				Want:  "",
			},
			{
				Name:  "Single",
				Input: "a",
				Want:  "a",
			},
			{
				Name:  "Whitespace basic",
				Input: "a b",
				Want:  "(and a b)",
			},
			{
				Name:  "Basic",
				Input: "a and b and c",
				Want:  "(and a b c)",
			},
			{
				Input: "aorb",
				Want:  "aorb",
			},
			{
				Input: "aANDb",
				Want:  "aANDb",
			},*/
		{
			Name:  "Reduced complex query mixed caps",
			Input: "a and b AND c or d and (e OR f) g repo:foo h i or j",
			Want:  "(or (and a b c) (and d repo:foo (concat (or e f) g h i)) j)",
		},
		{
			Name:  "TODO: this should flatten to concat entirely",
			Input: "a b (c d)",
			Want:  "(concat a b c d)",
		},
		{
			Name:  "TODO: this is a nonsense case we should validate statically. even simplifying it doesn't make sense.",
			Input: "a b (repo:foo c d)",
			Want:  "(concat a b (and repo:foo (concat c d)))",
		},
		{
			Name:  "TODO: this should be (concat foo (bar and foobar) baz) or equivalent",
			Input: "foo (bar AND foobar) baz",
			Want:  "(concat foo (and bar foobar) baz)",
		},
		{
			Name:  "XXX",
			Input: "repo:foo (repo:bar (repo:baz a b c d e))",
			Want:  "(and repo:foo repo:bar repo:baz (concat a b c d e))",
		},
		{
			Name:  "XXX",
			Input: "repo:foo (repo:bar (repo:baz a b c d) e)",
			Want:  "(and repo:foo repo:bar (concat (and repo:baz (concat a b c d)) e))",
		},
		{
			Name:  "XXX",
			Input: "(a) repo:foo (b)",
			Want:  "(and repo:foo (concat a b))",
		},
		{
			Name:  "XXX",
			Input: "(a) repo:foo (b) d (e (f (file:bar) g)) h (i (j or file:baz k) l)",
			Want:  "(and repo:foo (concat a b d e (and file:bar (concat f g)) h i (or j (and file:baz k)) l))",
		},
		// If we do too generally, spaces will imply concat. But we only
		// want to imply concat for search patterns. Implicit "AND" is
		// otherwise fine, we don't want to lose that. Alternative, easy
		// way is that we can literally just collect and create concat
		// for search patterns at each level, and promote others to and.

		// The problem with doing this in reduce is that
		// what do we do about "a (b and c) d" because we can't
		// really express (concat a), it reduces to a. But that means that the
		// pattern before can't be (and (concat a) (b and c) (concat d)).
		// It needs to be (concat a (b and c) d). Now we can get that form,
		// but the tricky part is when hwe have field:value cases.

		// Thus, GOAL: only nested queries, or patterns can potentially
		// have concat applied. Sequences of patterns MUST have concat
		// applied. field:value should never be in a concat, but rather
		// in an AND or OR.

		/*
			{
				Name:  "Basic reduced complex query",
				Input: "a and b or c and d or e",
				Want:  "(or (and a b) (and c d) e)",
			},
			{
				Name:  "Reduced complex query, reduction over parens",
				Input: "(a and b or c and d) or e",
				Want:  "(or (and a b) (and c d) e)",
			},
			{
				Name:  "Reduced complex query, nested 'or' trickles up",
				Input: "(a and b or c) or d",
				Want:  "(or (and a b) c d)",
			},
			{
				Name:  "Reduced complex query, nested nested 'or' trickles up",
				Input: "(a and b or (c and d or f)) or e",
				Want:  "(or (and a b) (and c d) f e)",
			},
			{
				Name:  "No reduction on precedence defined by parens",
				Input: "(a and (b or c) and d) or e",
				Want:  "(or (and a (or b c) d) e)",
			},
			{
				Name:  "Paren reduction over operators",
				Input: "(((a b c))) and d",
				Want:  "(and a b c d)",
			},
			// Errors.
			{
				Name:  "Unbalanced",
				Input: "(foo) (bar",
				Want:  "unbalanced expression",
			},
			{
				Name:  "Incomplete expression",
				Input: "a or",
				Want:  "expected operand at 4",
			},
			{
				Name:  "Illegal expression on the right",
				Input: "a or or b",
				Want:  "expected operand at 5",
			},
			{
				Name:  "Illegal expression on the right, mixed operators",
				Input: "a and OR",
				Want:  "expected operand at 6",
			},
			{
				Name:  "Illegal expression on the left",
				Input: "or",
				Want:  "expected operand at 0",
			},
			{
				Name:  "Illegal expression on the left, multiple operators",
				Input: "or or or",
				Want:  "expected operand at 0",
			},
			// Reduction.
			{
				Name:  "paren reduction with ands",
				Input: "(a and b) and (c and d)",
				Want:  "(and a b c d)",
			},
			{
				Name:  "paren reduction with ors",
				Input: "(a or b) or (c or d)",
				Want:  "(or a b c d)",
			},
			{
				Name:  "nested paren reduction with whitespace",
				Input: "(((a b c))) d",
				Want:  "(and a b c d)",
			},
			{
				Name:  "left paren reduction with whitespace",
				Input: "(a b) c d",
				Want:  "(and a b c d)",
			},
			{
				Name:  "right paren reduction with whitespace",
				Input: "a b (c d)",
				Want:  "(and a b c d)",
			},
			{
				Name:  "grouped paren reduction with whitespace",
				Input: "(a b) (c d)",
				Want:  "(and a b c d)",
			},
			{
				Name:  "multiple grouped paren reduction with whitespace",
				Input: "(a b) (c d) (e f)",
				Want:  "(and a b c d e f)",
			},
			{
				Name:  "interpolated grouped paren reduction",
				Input: "(a b) c d (e f)",
				Want:  "(and a b c d e f)",
			},
			{
				Name:  "mixed interpolated grouped paren reduction",
				Input: "(a and b and (z or q)) and (c and d) and (e and f)",
				Want:  "(and a b (or z q) c d e f)",
			},
			// Parentheses.
			{
				Name:  "empty paren",
				Input: "()",
				Want:  "",
			},
			{
				Name:  "nested empty paren",
				Input: "(x())",
				Want:  "x",
			},
			{
				Name:  "interpolated nested empty paren",
				Input: "(()x(  )(())())",
				Want:  "x",
			},
			{
				Name:  "empty paren on or",
				Input: "() or ()",
				Want:  "",
			},
			{
				Name:  "empty left paren on or",
				Input: "() or (x)",
				Want:  "x",
			},
			{
				Name:  "complex interpolated nested empty paren",
				Input: "(()x(  )(y or () or (f))())",
				Want:  "(and x (or y f))",
			},
		*/
	}
	for _, tt := range cases {
		t.Run(tt.Name, func(t *testing.T) {
			result, err := Parse(tt.Input)
			if err != nil {
				if diff := cmp.Diff(tt.Want, err.Error()); diff != "" {
					t.Fatal(diff)
				}
				return
			}
			var resultStr []string
			for _, node := range result {
				resultStr = append(resultStr, node.String())
			}
			got := strings.Join(resultStr, " ")
			if diff := cmp.Diff(tt.Want, got); diff != "" {
				t.Error(diff)
			}
		})
	}
}
