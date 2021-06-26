package control

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/mandelsoft/spiff/dynaml"
	"github.com/mandelsoft/spiff/yaml"
)

func init() {
	dynaml.RegisterControl("for", flowFor, "*do", "*mapkey")
}

type iterator interface {
	Len() int
	Index(int) interface{}
	Value(int) yaml.Node
}

type iteration struct {
	name    string
	index   string
	current int
	iterator
}

type iterations []*iteration

func (this iterations) Len() int {
	return len(this)
}
func (this iterations) Less(i, j int) bool {
	c := strings.Compare(this[i].name, this[j].name)
	if c != 0 {
		return c > 0
	}
	return strings.Compare(this[i].index, this[j].index) > 0
}

func (this iterations) Swap(i, j int) {
	this[i], this[j] = this[j], this[i]
}

func newIteration(name, index string, it iterator) iteration {
	if index == "" {
		index = "index-" + name
	}
	return iteration{name, index, 0, it}
}

func (this *iteration) IndexName() string {
	return this.index
}

func (this *iteration) Value() yaml.Node {
	return this.iterator.Value(this.current)
}

func (this *iteration) Index() interface{} {
	return this.iterator.Index(this.current)
}

///////////////////////////////

type listIterator struct {
	values []yaml.Node
}

func newListIterator(values []yaml.Node) iterator {
	return &listIterator{values}
}

func (this *listIterator) Len() int {
	return len(this.values)
}

func (this *listIterator) Index(i int) interface{} {
	return int64(i)
}

func (this *listIterator) Value(i int) yaml.Node {
	return this.values[i]
}

///////////////////////////////

type mapIterator struct {
	values map[string]yaml.Node
	keys   []string
}

func newMapIterator(values map[string]yaml.Node) iterator {
	return &mapIterator{values, yaml.GetSortedKeys(values)}
}

func (this *mapIterator) Len() int {
	return len(this.values)
}

func (this *mapIterator) Index(i int) interface{} {
	return this.keys[i]
}

func (this *mapIterator) Value(i int) yaml.Node {
	return this.values[this.keys[i]]
}

func flowFor(val yaml.Node, node yaml.Node, fields, opts map[string]yaml.Node, env dynaml.Binding) (yaml.Node, bool) {
	for range fields {
		return dynaml.ControlIssue("for", node, "no regular fields allowed in for control")
	}
	if !dynaml.IsResolvedNode(val, env) {
		return node, false
	}
	body, ok := opts["do"]
	if !ok {
		return dynaml.ControlIssue("for", node, "do fields required in for control")
	}
	if !dynaml.IsResolvedNode(body, env) {
		return node, false
	}

	var mapkey *dynaml.SubstitutionExpr
	k, ok := opts["mapkey"]
	if ok {
		if !dynaml.IsResolvedNode(k, env) {
			return node, false
		}
		if t, ok := k.Value().(dynaml.TemplateValue); ok {
			mapkey = &dynaml.SubstitutionExpr{dynaml.ValueExpr{t}}
		} else {
			return dynaml.ControlIssue("for", node, "mapkey must be an expression")
		}
	}

	var subst *dynaml.SubstitutionExpr
	if t, ok := body.Value().(dynaml.TemplateValue); ok {
		subst = &dynaml.SubstitutionExpr{dynaml.ValueExpr{t}}
	}
	ranges := iterations{}
	switch def := val.Value().(type) {
	case map[string]yaml.Node:
		ranges = make(iterations, len(def))
		i := 0
		for v, values := range def {
			i++
			name := ""
			index := ""
			parts := strings.Split(v, ",")
			switch len(parts) {
			case 2:
				index = strings.TrimSpace(parts[0])
				name = strings.TrimSpace(parts[1])
			case 1:
				name = strings.TrimSpace(parts[0])
			default:
				return dynaml.ControlIssue("for", node, "invalid control variable spec %q", v)
			}
			it, err := controlIterator(name, values)
			if err != nil {
				return dynaml.ControlIssue("for", node, err.Error())
			}
			ranges[len(ranges)-i], err = controlIteration(name, index, it)
			if err != nil {
				return dynaml.ControlIssue("for", node, err.Error())
			}
		}
		sort.Sort(ranges)
	case []yaml.Node:
		ranges = make(iterations, len(def))
		for i, v := range def {
			spec, ok := v.Value().(map[string]yaml.Node)
			if !ok {
				return dynaml.ControlIssue("for", node, "control variable list entry requires may but got %s", dynaml.ExpressionType(v.Value()))
			}
			n := spec["name"]
			if n == nil {
				return dynaml.ControlIssue("for", node, "control variable list entry requires name field")
			}
			name, ok := n.Value().(string)
			index := ""
			n = spec["index"]
			if n != nil {
				index, ok = n.Value().(string)
				if !ok {
					return dynaml.ControlIssue("for", node, "control index variable name must be of type string but got %s", dynaml.ExpressionType(n.Value()))
				}
			}
			l := spec["values"]
			if l == nil {
				return dynaml.ControlIssue("for", node, "control variable list entry requires values field")
			}
			it, err := controlIterator(name, l)
			if err != nil {
				return dynaml.ControlIssue("for", node, err.Error())
			}

			if len(spec) < 2 || len(spec) > 3 {
				return dynaml.ControlIssue("for", node, "control variable list entry requires two or three fields: name, values and optionally index")
			}

			if len(spec) == 3 && index == "" {
				for _, k := range yaml.GetSortedKeys(spec) {
					switch k {
					case "name":
					case "values":
					case "index":
					default:
						return dynaml.ControlIssue("for", node, "invalid control variable list entry field %q", k)
					}
				}
			}

			ranges[len(ranges)-i-1], err = controlIteration(name, index, it)
			if err != nil {
				return dynaml.ControlIssue("for", node, err.Error())
			}
		}
	default:
		return dynaml.ControlIssue("for", node, "value field must be map but got %s", dynaml.ExpressionType(def))
	}

	var resultlist []yaml.Node
	var resultmap map[string]yaml.Node

	if mapkey != nil {
		resultmap = map[string]yaml.Node{}
	} else {
		resultlist = []yaml.Node{}
	}

	done := true
	issue := yaml.Issue{}
outer:
	for {
		// do
		inp := map[string]yaml.Node{}
		for i := 0; i < len(ranges); i++ {
			inp[ranges[i].name] = ranges[i].Value()
			inp[ranges[i].IndexName()] = yaml.NewNode(ranges[i].Index(), "for")
		}
		scope := env.WithLocalScope(inp)
		key := ""
		if mapkey != nil {
			k, info, ok := mapkey.Evaluate(scope, false)
			if !ok {
				done = false
				issue.Nested = append(issue.Nested, controlVariablesIssue(ranges, info.Issue))
			}
			if key, ok = k.(string); !ok {
				done = false
				issue.Nested = append(issue.Nested, controlVariablesIssue(ranges, yaml.NewIssue("map key must be string, but found %s", dynaml.ExpressionType(k))))
			}
		}
		if subst != nil {
			v, info, ok := subst.Evaluate(scope, false)
			if !ok {
				done = false
				issue.Nested = append(issue.Nested, controlVariablesIssue(ranges, info.Issue))
			} else {
				if dynaml.IsExpression(v) {
					done = false
				} else {
					if mapkey != nil {
						resultmap[key] = yaml.NewNode(v, node.SourceName())
					} else {
						resultlist = append(resultlist, yaml.NewNode(v, node.SourceName()))
					}
				}
			}
		} else {
			if mapkey != nil {
				resultmap[key] = opts["do"]
			} else {
				resultlist = append(resultlist, opts["do"])
			}
		}

		for i := 0; i <= len(ranges); i++ {
			if i == len(ranges) {
				break outer
			}
			ranges[i].current++
			if ranges[i].current < ranges[i].Len() {
				break
			}
			ranges[i].current = 0
		}
	}
	if !done {
		if len(issue.Nested) > 0 {
			issue.Issue = "error evaluationg for body"
			return dynaml.ControlIssueByIssue("for", node, issue, false)
		}
		return node, false
	}
	if resultlist != nil {
		return yaml.NewNode(resultlist, node.SourceName()), true
	}
	return yaml.NewNode(resultmap, node.SourceName()), true
}

var namesyntax = regexp.MustCompile("[a-zA-Z0-9_]+")

func checkName(kind, n string) error {
	if !namesyntax.Match([]byte(n)) {
		return fmt.Errorf("invalid %s variable name %q (must be %s)", kind, n, namesyntax.String())
	}
	return nil
}

func controlIteration(name, index string, it iterator) (*iteration, error) {
	if err := checkName("range", name); err != nil {
		return nil, err
	}
	if index == "" {
		index = "index-" + name
	} else {
		if err := checkName("index", index); err != nil {
			return nil, err
		}
	}
	return &iteration{name, index, 0, it}, nil
}

func controlIterator(name string, val yaml.Node) (iterator, error) {
	var it iterator
	switch values := val.Value().(type) {
	case []yaml.Node:
		if len(values) == 0 {
			return nil, nil
		}
		it = newListIterator(values)
	case map[string]yaml.Node:
		if len(values) == 0 {
			return nil, nil
		}
		it = newMapIterator(values)
	default:
		return nil, fmt.Errorf("control variable %q requires list or map value, but got %s", name, dynaml.ExpressionType(val.Value()))
	}
	return it, nil
}

func controlVariablesIssue(iterations iterations, issue yaml.Issue) yaml.Issue {
	desc := fmt.Sprintf("control variables: ")
	sep := ""
	for _, i := range iterations {
		desc = fmt.Sprintf("%s%s %s[%v]=%s", desc, sep, i.name, i.Index(), dynaml.Shorten(dynaml.Short(i.Value().Value(), false)))
		sep = ";"
	}
	issue.Issue = fmt.Sprintf("%s: %s", desc, issue.Issue)
	return issue
}
