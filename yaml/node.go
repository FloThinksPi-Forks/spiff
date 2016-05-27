package yaml

import (
	"fmt"
	"reflect"
	"regexp"

	"github.com/cloudfoundry-incubator/candiedyaml"
)

type Node interface {
	candiedyaml.Marshaler

	Value() interface{}
	SourceName() string
	RedirectPath() []string
	Temporary() bool
	ReplaceFlag() bool
	Preferred() bool
	Merged() bool
	KeyName() string
	HasError() bool
	Failed() bool
	Undefined() bool
	Issue() Issue

	GetAnnotation() Annotation
	EquivalentToNode(Node) bool
}

type AnnotatedNode struct {
	value      interface{}
	sourceName string
	Annotation
}

type Issue struct {
	Issue  string
	Nested []Issue
}

func NewIssue(msg string, args ...interface{}) Issue {
	return Issue{fmt.Sprintf(msg, args...), []Issue{}}
}

type Annotation struct {
	redirectPath []string
	temporary    bool
	replace      bool
	preferred    bool
	merged       bool
	keyName      string
	error        bool
	failed       bool
	undefined    bool
	issue        Issue
}

func NewNode(value interface{}, sourcePath string) Node {
	return AnnotatedNode{MassageType(value), sourcePath, EmptyAnnotation()}
}

func ReplaceValue(value interface{}, node Node) Node {
	return AnnotatedNode{value, node.SourceName(), node.GetAnnotation()}
}
func ReferencedNode(node Node) Node {
	return AnnotatedNode{node.Value(), node.SourceName(), NewReferencedAnnotation(node)}
}

func SubstituteNode(value interface{}, node Node) Node {
	return AnnotatedNode{MassageType(value), node.SourceName(), node.GetAnnotation()}
}

func RedirectNode(value interface{}, node Node, redirect []string) Node {
	return AnnotatedNode{MassageType(value), node.SourceName(), node.GetAnnotation().SetRedirectPath(redirect)}
}

func ReplaceNode(value interface{}, node Node, redirect []string) Node {
	return AnnotatedNode{MassageType(value), node.SourceName(), node.GetAnnotation().SetReplaceFlag().SetRedirectPath(redirect)}
}

func PreferredNode(node Node) Node {
	return AnnotatedNode{node.Value(), node.SourceName(), node.GetAnnotation().SetPreferred()}
}

func MergedNode(node Node) Node {
	return AnnotatedNode{node.Value(), node.SourceName(), node.GetAnnotation().SetMerged()}
}

func KeyNameNode(node Node, keyName string) Node {
	return AnnotatedNode{node.Value(), node.SourceName(), node.GetAnnotation().AddKeyName(keyName)}
}

func IssueNode(node Node, error bool, failed bool, issue Issue) Node {
	return AnnotatedNode{node.Value(), node.SourceName(), node.GetAnnotation().AddIssue(error, failed, issue)}
}

func UndefinedNode(node Node) Node {
	return AnnotatedNode{node.Value(), node.SourceName(), node.GetAnnotation().SetUndefined()}
}

func TemporaryNode(node Node) Node {
	return AnnotatedNode{node.Value(), node.SourceName(), node.GetAnnotation().SetTemporary()}
}

func MassageType(value interface{}) interface{} {
	switch value.(type) {
	case int, int8, int16, int32:
		value = reflect.ValueOf(value).Int()
	}
	return value
}

func EmptyAnnotation() Annotation {
	return Annotation{nil, false, false, false, false, "", false, false, false, Issue{}}
}

func NewReferencedAnnotation(node Node) Annotation {
	return Annotation{nil, false, false, false, false, node.KeyName(), node.HasError(), node.Failed(), node.Undefined(), node.Issue()}
}

func (n Annotation) RedirectPath() []string {
	return n.redirectPath
}

func (n Annotation) Temporary() bool {
	return n.temporary
}

func (n Annotation) ReplaceFlag() bool {
	return n.replace
}

func (n Annotation) Preferred() bool {
	return n.preferred
}

func (n Annotation) Merged() bool {
	return n.merged || n.ReplaceFlag() || len(n.RedirectPath()) > 0
}

func (n Annotation) KeyName() string {
	return n.keyName
}

func (n Annotation) HasError() bool {
	return n.error
}

func (n Annotation) Failed() bool {
	return n.failed
}

func (n Annotation) Undefined() bool {
	return n.undefined
}

func (n Annotation) Issue() Issue {
	return n.issue
}

func (n Annotation) SetTemporary() Annotation {
	n.temporary = true
	return n
}

func (n Annotation) SetRedirectPath(redirect []string) Annotation {
	n.redirectPath = redirect
	return n
}

func (n Annotation) SetReplaceFlag() Annotation {
	n.replace = true
	return n
}

func (n Annotation) SetPreferred() Annotation {
	n.preferred = true
	return n
}

func (n Annotation) SetMerged() Annotation {
	n.merged = true
	return n
}

func (n Annotation) SetUndefined() Annotation {
	n.undefined = true
	return n
}

func (n Annotation) AddKeyName(keyName string) Annotation {
	if keyName != "" {
		n.keyName = keyName
	}
	return n
}

func (n Annotation) AddIssue(error bool, failed bool, issue Issue) Annotation {
	if issue.Issue != "" {
		n.issue = issue
	}
	n.error = error
	n.failed = failed
	return n
}

func (n AnnotatedNode) Value() interface{} {
	return n.value
}

func (n AnnotatedNode) SourceName() string {
	return n.sourceName
}

func (n AnnotatedNode) GetAnnotation() Annotation {
	return n.Annotation
}

func (n AnnotatedNode) MarshalYAML() (string, interface{}, error) {
	return "", n.Value(), nil
}

func (n AnnotatedNode) EquivalentToNode(o Node) bool {
	if o == nil {
		return false
	}

	at := reflect.TypeOf(n.Value())
	bt := reflect.TypeOf(o.Value())

	if at != bt {
		return false
	}

	switch nv := n.Value().(type) {
	case map[string]Node:
		ov := o.Value().(map[string]Node)

		if len(nv) != len(ov) {
			return false
		}

		for key, nval := range nv {
			oval, found := ov[key]
			if !found {
				return false
			}

			if !nval.EquivalentToNode(oval) {
				return false
			}
		}

		return true

	case []Node:
		ov := o.Value().([]Node)

		if len(nv) != len(ov) {
			return false
		}

		for i, nval := range nv {
			oval := ov[i]

			if !nval.EquivalentToNode(oval) {
				return false
			}
		}

		return true
	}

	b := reflect.DeepEqual(n.Value(), o.Value())

	return b
}

var embeddedDynaml = regexp.MustCompile(`^\(\((.*)\)\)$`)

func EmbeddedDynaml(root Node) *string {
	rootString := root.Value().(string)

	sub := embeddedDynaml.FindStringSubmatch(rootString)
	if sub == nil {
		return nil
	}
	return &sub[1]
}
