package flow

import (
	"fmt"
	"strings"

	"github.com/mandelsoft/spiff/debug"
	"github.com/mandelsoft/spiff/dynaml"
	"github.com/mandelsoft/spiff/yaml"

	_ "github.com/mandelsoft/spiff/dynaml/passwd"
	_ "github.com/mandelsoft/spiff/dynaml/semver"
	_ "github.com/mandelsoft/spiff/dynaml/wireguard"
	_ "github.com/mandelsoft/spiff/dynaml/x509"
)

func Flow(source yaml.Node, stubs ...yaml.Node) (yaml.Node, error) {
	return NestedFlow(nil, source, stubs...)
}

func NestedFlow(outer dynaml.Binding, source yaml.Node, stubs ...yaml.Node) (yaml.Node, error) {
	env := NewNestedEnvironment(stubs, source.SourceName(), outer)
	defer CleanupEnvironment(env)
	return env.Flow(source, true)
}

func get_inherited_flags(env dynaml.Binding) (yaml.NodeFlags, yaml.Node) {
	overridden, found := env.FindInStubs(env.StubPath())
	if found {
		return overridden.Flags() & (yaml.FLAG_TEMPORARY | yaml.FLAG_STATE), overridden
	}
	return 0, overridden
}

func flow(root yaml.Node, env dynaml.Binding, shouldOverride, enforceTemplate bool) yaml.Node {
	node := _flow(root, env, shouldOverride, enforceTemplate)
	tag := node.GetAnnotation().Tag()
	if tag != "" {
		debug.Debug("found tag %q at %v\n", tag, env.Path())
	}
	if dynaml.IsResolvedNode(node, env) && tag != "" {
		scope := dynaml.TAG_LOCAL
		if strings.HasPrefix(tag, "*") {
			tag = tag[1:]
			scope |= dynaml.TAG_SCOPE_GLOBAL
		} else {
			scope |= dynaml.TAG_SCOPE_STREAM
		}
		err := env.GetState().SetTag(tag, node, env.Path(), scope)
		if err != nil {
			if node.Value() == nil {
				node = yaml.ReplaceValue(root.Value(), node)
			}
			node = yaml.IssueNode(node, true, true, yaml.NewIssue("%s", err))
		}
	}
	return node
}

func _flow(root yaml.Node, env dynaml.Binding, shouldOverride, enforceTemplate bool) yaml.Node {
	if root == nil {
		return root
	}

	flags := root.Flags()
	replace := root.ReplaceFlag()
	redirect := root.RedirectPath()
	preferred := root.Preferred()
	merged := root.Merged()
	keyName := root.KeyName()
	source := root.SourceName()
	template := root.Template()

	if redirect != nil {
		env = env.RedirectOverwrite(redirect)
	}

	debug.Debug("//{ FLOW %v: %+v\n", env.Path(), root)
	debug.Debug("/// BIND: %+v\n", env)
	defer debug.Debug("//}\n")
	if !replace {
		if _, ok := root.Value().(dynaml.Expression); !ok && merged {
			debug.Debug("  skip handling of merged node")
			return root
		}
		switch val := root.Value().(type) {
		case map[string]yaml.Node:
			ok, err := dynaml.IsControl(root, env)
			if err != nil {
				return dynaml.IssueNode(env, true, root, true, true, yaml.NewIssue("%s", err))
			}
			root = flowMap(root, env, !ok, enforceTemplate)
			if !ok {
				return root
			} else {
				if _, ok := root.Value().(map[string]yaml.Node); ok {
					return root
				}
				// handle override
			}

		case []yaml.Node:
			return flowList(root, env, enforceTemplate)

		case dynaml.Expression:
			debug.Debug("??? eval %T: %+v\n", val, val)
			env := env
			if root.SourceName() != env.SourceName() {
				env = env.WithSource(root.SourceName())
			}
			var eval interface{} = nil
			info := dynaml.DefaultInfo()

			m, ok := asTemplate(val, enforceTemplate)
			if ok {
				if tag := m.GetTag(); tag != "" && root.GetAnnotation().Tag() == "" {
					root = yaml.SetTag(root, tag)
				}
			}
			if ok && m.Has(dynaml.TEMPLATE) {
				debug.Debug("found template declaration\n")
				tval := m.TemplateExpression(root)
				if tval == nil {
					info.SetError("empty template value")
					debug.Debug("??? failed ---> KEEP\n")
					if !shouldOverride {
						return dynaml.IssueNode(env, true, root, true, false, info.Issue)
					}
					ok = false
				} else {
					debug.Debug("  value template %s", tval)
					eval = dynaml.NewTemplateValue(env.Path(), tval, root, env)
				}
				flags |= m.GetFlags()
			} else {
				eval, info, ok = val.Evaluate(env, false)
				if err := info.Cleanup(); err != nil {
					info.SetError("%s", err)
					eval = nil
					ok = false
				}
				if info.RedirectPath != nil {
					debug.Debug("eval found redirect %v, %v", info.RedirectPath, ok)
				}
			}
			flags |= info.NodeFlags
			if flags.Dynamic() {
				if _, tok := eval.(dynaml.TemplateValue); !tok && template == nil {
					info.SetError("dynamic marker for non-template value node")
					debug.Debug("??? invalid dynamic ---> KEEP\n")
					if !shouldOverride {
						return dynaml.IssueNode(env, true, root, true, false, info.Issue)
					}
					ok = false
				}
				if template == nil {
					eval, template = substituteValue(eval, flags)
				}
			}
			replace = replace || info.Replace
			debug.Debug("??? ---> %t %#v\n", ok, eval)
			if !ok {
				root = dynaml.IssueNode(env, true, root, true, false, info.Issue)
				debug.Debug("??? failed ---> KEEP\n")
				if !shouldOverride {
					return root
				}
			} else {
				if info.SourceName() != "" {
					source = info.SourceName()
				}
				tag := root.GetAnnotation().Tag()
				var result yaml.Node
				if template != nil {
					result = yaml.NewDynamicNode(eval, template, source)
				} else {
					result = yaml.NewNode(eval, source)
				}
				_, ok = eval.(string)
				if ok {
					// map result to potential expression
					result, _ = FlowString(result, env)
				}
				_, expr := result.Value().(dynaml.Expression)

				if len(info.Issue.Issue) != 0 {
					result = dynaml.IssueNode(env, true, result, false, info.Failed, info.Issue)
				}
				if info.Undefined {
					debug.Debug("   UNDEFINED")
					result = yaml.UndefinedNode(result)
				}
				// preserve accumulated node attributes
				if preferred || info.Preferred {
					debug.Debug("   PREFERRED")
					result = yaml.PreferredNode(result)
				}

				if info.KeyName != "" {
					keyName = info.KeyName
					result = yaml.KeyNameNode(result, keyName)
				}
				if info.RedirectPath != nil {
					redirect = info.RedirectPath
					debug.Debug("found redirect %v", redirect)
				}
				if redirect != nil {
					debug.Debug("   REDIRECT -> %v\n", redirect)
					result = yaml.RedirectNode(result.Value(), result, redirect)
				}

				if replace {
					debug.Debug("   REPLACE\n")
					result = yaml.ReplaceNode(result.Value(), result, redirect)
				} else {
					if merged || info.Merged {
						debug.Debug("   MERGED\n")
						result = yaml.MergedNode(result)
					}
				}

				result = updateNode(result, flags, tag)
				if expr || result.Merged() || !shouldOverride || result.Preferred() {
					debug.Debug("   prefer expression over override")
					debug.Debug("??? ---> %+v\n", result)
					return result
				}
				debug.Debug("???   try override\n")
				replace = result.ReplaceFlag()
				root = result
			}

		case string:
			result, _ := FlowString(root, env)
			if result != nil {
				_, ok := result.Value().(dynaml.Expression)
				if ok {
					// analyse expression before overriding
					return result
				}
			}
		}
	}

	if !merged && root.StandardOverride() && shouldOverride && !env.NoMerge() {
		debug.Debug("/// lookup stub %v -> %v\n", env.Path(), env.StubPath())
		overridden, found := env.FindInStubs(env.StubPath())
		if found && !overridden.Flags().Default() && !root.Flags().Injected() {
			root, _ = substituteNode(overridden)
			if keyName != "" {
				root = yaml.KeyNameNode(root, keyName)
			}
			if replace {
				root = yaml.ReplaceNode(root.Value(), root, redirect)
			} else {
				if redirect != nil {
					root = yaml.RedirectNode(root.Value(), root, redirect)
				} else {
					if merged {
						root = yaml.MergedNode(root)
					}
				}
			}
			root = yaml.AddFlags(root, flags.Overridden())
		}
	}

	debug.Debug("result: %#v\n", root)
	return root
}

/*
 * compatibility issue. A single merge node was always optional
 * means: <<: (( merge )) == <<: (( merge || nil ))
 * the first pass, just parses the dynaml
 * only the second pass, evaluates a dynaml node!
 */
func simpleMergeCompatibilityCheck(initial bool, node yaml.Node) bool {
	if !initial {
		merge, ok := node.Value().(dynaml.MergeExpr)
		return ok && !merge.Required
	}
	return false
}

func flowMap(root yaml.Node, env dynaml.Binding, shouldOverride, template bool) yaml.Node {
	var err error
	flags, stub := get_inherited_flags(env)
	tag := root.GetAnnotation().Tag()
	processed := true
	merged := false
	issue, failed := root.Issue(), root.Failed()
	rootMap := root.Value().(map[string]yaml.Node)

	rootEnv := env
	env = env.WithScope(rootMap)

	redirect := root.RedirectPath()
	replace := root.ReplaceFlag()
	newMap := make(map[string]yaml.Node)
	undefined := make(map[string]yaml.Node)

	debug.Debug("HANDLE MAP %v (template=%t)\n", env.Path(), template)
	addEntries := true

	marker := dynaml.NewTemplateMarker(nil)
	mergekey := "<<"
	mergeval, ok := rootMap[mergekey]
	if ok {
		if _, ok := rootMap[yaml.MERGEKEY]; ok {
			return yaml.IssueNode(root, true, true, yaml.NewIssue("multiple merge keys not allowed"))
		}
	} else {
		mergekey = yaml.MERGEKEY
		mergeval, ok = rootMap[yaml.MERGEKEY]
	}

	if ok {
		val := mergeval
		debug.Debug("handle map merge %#v\n", val)
		_, initial := val.Value().(string)
		base := _flow(val, env, false, false)
		if base.Undefined() {
			return yaml.UndefinedNode(root)
		}
		debug.Debug("flow to %#v\n", base.Value())
		e, ok := base.Value().(dynaml.Expression)
		if ok {
			marker, ok = asTemplate(e, template)
			if ok {
				debug.Debug("found marker\n")
				if t := marker.GetTag(); t != "" {
					debug.Debug("found tag %q\n", t)
					tag = t
				}
				flags |= marker.GetFlags()
				if flags.Temporary() {
					debug.Debug("found temporary declaration\n")
				}
				if flags.Local() {
					debug.Debug("found static declaration\n")
				}
				if flags.Default() {
					debug.Debug("found default declaration\n")
				}
			}
			if ok && marker.Has(dynaml.TEMPLATE) {
				template = true
				val = marker.TemplateExpression(root)
				if val != nil {
					debug.Debug("  insert expression: %v\n", val)
				}
			} else {
				if simpleMergeCompatibilityCheck(initial, base) {
					debug.Debug("  skip merge\n")
					val = nil
				} else {
					debug.Debug("  continue merge\n")
					processed = false
					val = base
				}
			}
		} else {
			if base == nil {
				debug.Debug("base is nil\n")
			} else {
				if base.RedirectPath() != nil {
					debug.Debug("redirected: %v, merged %v", base.RedirectPath(), base.Merged())
					redirect = base.RedirectPath()
					env = env.RedirectOverwrite(redirect)
				}
			}
			if base.Merged() {
				merged = true
			}

			baseMap, ok := base.Value().(map[string]yaml.Node)
			if ok {
				for k, v := range baseMap {
					newMap[k] = v
				}
			}
			// still ignore non dynaml value (might be strange but compatible)
			replace = base.ReplaceFlag()
			parseError := yaml.EmbeddedDynaml(base, env.GetState().InterpolationEnabled()) != nil
			if !ok && base.Value() != nil && !parseError {
				err = fmt.Errorf("require map value for '<<' insert, found '%s'", dynaml.ExpressionType(base.Value()))
			}
			if ok || base.Value() == nil || !parseError {
				val = nil
				if replace {
					addEntries = false
				}
			} else {
				val = base
			}
		}

		// handle value
		mergeval = val
	}

	if template {
		debug.Debug("found template declaration\n")
		processed = false
	}

	if addEntries {
		sortedKeys := yaml.GetSortedKeys(rootMap)
		for i := range sortedKeys {
			key := sortedKeys[i]
			val := rootMap[key]

			if key == mergekey {
				val = mergeval
				if val == nil {
					continue
				}
			} else {
				if processed {
					val = flow(val, env.WithPath(key), shouldOverride, dynaml.RequireTemplate(key, env))
				} else {
					debug.Debug("skip %q flow for unprocessed indication\n", key)
				}
			}

			debug.Debug("MAP %v (%s)%s  -> %T\n", env.Path(), val.KeyName(), key, val.Value())
			if !val.Undefined() {
				if flags.PropagateImplied() {
					val = yaml.AddFlags(val, yaml.FLAG_IMPLIED)
				}
				newMap[key] = val
			} else {
				undefined[key] = val
			}
		}
	}

	debug.Debug("MAP DONE %v\n", env.Path())
	if merged {
		flags |= yaml.FLAG_INJECTED
	} else {
		if stub != nil && !flags.Injected() {
			if m, ok := stub.Value().(map[string]yaml.Node); ok {
				for k, v := range m {
					if v.Flags().Inject() && newMap[k] == nil {
						v, _ = substituteNode(v)
						newMap[k] = yaml.AddFlags(v, yaml.FLAG_INJECT|yaml.FLAG_INJECTED)
					}
				}
			}
			//flags |= yaml.FLAG_INJECTED
		}
	}
	var result interface{}
	if template {
		debug.Debug(" as template\n")
		result = dynaml.NewTemplateValue(env.Path(), yaml.NewNode(newMap, root.SourceName()), root, rootEnv)
	} else {
		result = newMap
	}
	var node yaml.Node
	if replace {
		node = yaml.ReplaceNode(result, root, redirect)
	} else {
		node = yaml.RedirectNode(result, root, redirect)
	}

	if err != nil || failed {
		if err != nil {
			node = yaml.IssueNode(node, true, true, yaml.NewIssue("%s", err))
		} else {
			node = yaml.IssueNode(node, true, true, issue)
		}
	} else {
		node, _, _ = flowControl(node, undefined, env)
	}
	return updateNode(node, flags, tag)
}

func flowList(root yaml.Node, env dynaml.Binding, template bool) yaml.Node {
	rootList := root.Value().([]yaml.Node)

	debug.Debug("HANDLE LIST %v\n", env.Path())
	merged, process, replaced, redirectPath, keyName, ismerged, flags, tag, stub := processMerges(root, rootList, env, template)

	if process {
		debug.Debug("process list (key: %s) %v\n", keyName, env.Path())
		newList := []yaml.Node{}
		if redirectPath != nil {
			env = env.RedirectOverwrite(redirectPath)
		}
		for idx, val := range merged.([]yaml.Node) {
			step, resolved := stepName(idx, val, keyName, env)
			debug.Debug("  step %s\n", step)
			if resolved {
				val = flow(val, env.WithPath(step), false, false)
			}
			if !val.Undefined() {
				newList = append(newList, val)
			}
		}
		if ismerged {
			flags |= yaml.FLAG_INJECTED
		} else {
			if stub != nil && !root.Flags().Injected() {
				if m, ok := stub.Value().([]yaml.Node); ok {
					injected := []yaml.Node{}
					for _, v := range m {
						if v.Flags().Inject() {
							injected = append(injected, v)
						}
					}
					newList = append(injected, newList...)
				}
				flags |= yaml.FLAG_INJECTED
			}
		}

		merged = newList
	}

	if keyName != "" {
		root = yaml.KeyNameNode(root, keyName)
	}
	debug.Debug("LIST DONE (%s)%v\n", root.KeyName(), env.Path())

	if replaced {
		root = yaml.ReplaceNode(merged, root, redirectPath)
	} else {
		if redirectPath != nil {
			root = yaml.RedirectNode(merged, root, redirectPath)
		} else {
			root = yaml.SubstituteNode(merged, root)
		}
	}

	if (flags | root.Flags()) != root.Flags() {
		return yaml.AddFlags(root, flags)
	}
	if tag != "" && tag != root.GetAnnotation().Tag() {
		root = yaml.SetTag(root, tag)
	}
	return root
}

func FlowString(root yaml.Node, env dynaml.Binding) (yaml.Node, error) {

	sub := yaml.EmbeddedDynaml(root, env.GetState().InterpolationEnabled())
	if sub == nil {
		return root, nil
	}
	expr, err := dynaml.Parse(*sub, env.Path(), env.StubPath())
	if err != nil {
		debug.Debug("parse dynaml: %v: %s failed: %s\n", env.Path(), *sub, err)
		return root, err
	}
	debug.Debug("parse dynaml: %v: %s  -> %T\n", env.Path(), *sub, expr)

	return yaml.SubstituteNode(expr, root), nil
}

func stepName(index int, value yaml.Node, keyName string, env dynaml.Binding) (string, bool) {
	if keyName == "" {
		keyName = "name"
	}
	name, ok := yaml.FindString(value, env.GetFeatures(), keyName)
	if ok {
		return keyName + ":" + name, true
	}

	step := fmt.Sprintf("[%d]", index)
	v, ok := yaml.FindR(true, value, env.GetFeatures(), keyName)
	if ok && v.Value() != nil {
		debug.Debug("found raw %s", keyName)
		_, ok := v.Value().(dynaml.Expression)
		if ok {
			v = flow(v, env.WithPath(step), false, false)
			_, ok := v.Value().(dynaml.Expression)
			if ok {
				return step, false
			}
		}
		name, ok = v.Value().(string)
		if ok {
			return keyName + ":" + name, true
		}
	} else {
		debug.Debug("raw %s not found", keyName)
	}
	return step, true
}

func processMerges(orig yaml.Node, root []yaml.Node, env dynaml.Binding, template bool) (interface{}, bool, bool, []string, string, bool, yaml.NodeFlags, string, yaml.Node) {
	var flags yaml.NodeFlags
	var stub yaml.Node
	flags, stub = get_inherited_flags(env)
	tag := orig.GetAnnotation().Tag()
	spliced := []yaml.Node{}
	process := true
	merged := false
	keyName := orig.KeyName()
	replaced := orig.ReplaceFlag()
	redirectPath := orig.RedirectPath()

	for _, val := range root {
		if val == nil {
			continue
		}

		inlineNode, qual, ok := yaml.UnresolvedListEntryMerge(val)
		if ok {
			debug.Debug("*** %+v\n", inlineNode.Value())
			_, initial := inlineNode.Value().(string)
			result := _flow(inlineNode, env, false, false)
			if result.KeyName() != "" {
				keyName = result.KeyName()
			}
			debug.Debug("=== (%s)%+v\n", keyName, result)
			e, ok := result.Value().(dynaml.Expression)
			if ok {
				if simpleMergeCompatibilityCheck(initial, inlineNode) {
					continue
				}
				m, ok := asTemplate(e, template)
				if ok {
					flags |= m.GetFlags()
					if t := m.GetTag(); t != "" {
						tag = t
					}
					if ok && m.Has(dynaml.TEMPLATE) {
						debug.Debug("found template declaration\n")
						template = true
						process = false
						result = m.TemplateExpression(orig)
						if result == nil {
							continue
						}
						debug.Debug("  insert expression: %v\n", result)
					}
				}
				newMap := make(map[string]yaml.Node)
				newMap[qual] = result
				val = yaml.SubstituteNode(newMap, orig)
				process = false
			} else {
				inline, ok := result.Value().([]yaml.Node)

				if ok {
					merged = true
					inlineNew := newEntries(inline, root, keyName)
					replaced = result.ReplaceFlag()
					redirectPath = result.RedirectPath()
					if replaced {
						spliced = inlineNew
						process = false
						break
					} else {
						merged = true
						spliced = append(spliced, inlineNew...)
					}
				}
				if ok || result.Value() == nil || yaml.EmbeddedDynaml(result, env.GetState().InterpolationEnabled()) == nil {
					// still ignore non dynaml value (might be strange but compatible)
					redirectPath = result.RedirectPath()
					if result.Merged() {
						merged = true
					}
					continue
				}
			}
		}

		val, newKey := ProcessKeyTag(val)
		if newKey != "" {
			keyName = newKey
		}
		spliced = append(spliced, val)
	}

	var result interface{}
	if template {
		debug.Debug(" as template\n")
		result = dynaml.NewTemplateValue(env.Path(), yaml.NewNode(spliced, orig.SourceName()), orig, env)
	} else {
		processed := []yaml.Node{}
		for _, val := range spliced {
			ok, err := dynaml.IsControl(val, env)
			if err != nil {
				val = yaml.IssueNode(val, true, true, yaml.NewIssue("%s", err))
			} else if ok {
				val = _flow(val, env, false, false)
				if a, ok := val.Value().([]yaml.Node); ok {
					processed = append(processed, a...)
					continue
				} else {
					process = false
				}
			}
			processed = append(processed, val)
		}
		result = processed
	}

	debug.Debug("--> %+v  proc=%v replaced=%v redirect=%v key=%s\n", result, process, replaced, redirectPath, keyName)
	return result, process, replaced, redirectPath, keyName, merged, flags, tag, stub
}

func ProcessKeyTag(val yaml.Node) (yaml.Node, string) {
	keyName := ""

	m, ok := val.Value().(map[string]yaml.Node)
	if ok {
		found := false
		for key, _ := range m {
			split := strings.Index(key, ":")
			if split > 0 {
				if key[:split] == "key" {
					keyName = key[split+1:]
					found = true
				}
			}
		}
		if found {
			newMap := make(map[string]yaml.Node)
			for key, v := range m {
				split := strings.Index(key, ":")
				if split > 0 {
					if key[:split] == "key" {
						key = key[split+1:]
					}
				}
				newMap[key] = v
			}
			return yaml.SubstituteNode(newMap, val), keyName
		}
	}
	return val, keyName
}

func newEntries(a []yaml.Node, b []yaml.Node, keyName string) []yaml.Node {
	if keyName == "" {
		keyName = "name"
	}
	old := yaml.KeyNameNode(yaml.NewNode(b, "some map"), keyName)
	added := []yaml.Node{}

	for _, val := range a {
		name, ok := yaml.FindStringR(true, val, nil, keyName)
		if ok {
			_, found := yaml.FindR(true, old, nil, name) // TODO
			if found {
				continue
			}
		}

		added = append(added, val)
	}

	return added
}

func updateNode(node yaml.Node, flags yaml.NodeFlags, tag string) yaml.Node {
	if (flags | node.Flags()) != node.Flags() {
		node = yaml.AddFlags(node, flags)
	}
	if tag != "" && tag != node.GetAnnotation().Tag() {
		node = yaml.SetTag(node, tag)
	}
	return node
}

func substituteNode(v yaml.Node) (yaml.Node, bool) {
	t, ok := v.Value().(dynaml.TemplateValue)
	if !ok {
		t, ok = v.Template().(dynaml.TemplateValue)
	}
	if v.Flags().Dynamic() && ok {
		return yaml.AddFlags(yaml.NewDynamicNode(dynaml.SubstitutionExpr{dynaml.ValueExpr{t}}, t, "<substitute>"), v.Flags()), true
	}
	return v, false
}

func substituteValue(v interface{}, flags yaml.NodeFlags) (interface{}, interface{}) {
	t, ok := v.(dynaml.TemplateValue)

	if flags.Dynamic() && ok {
		return dynaml.SubstitutionExpr{dynaml.ValueExpr{t}}, t
	}
	return v, nil
}

func asTemplate(val dynaml.Expression, enforceTemplate bool) (dynaml.MarkerExpr, bool) {
	m, ok := val.(dynaml.MarkerExpr)
	if ok {
		if enforceTemplate {
			m.Add(dynaml.TEMPLATE)
		}
	} else {
		if enforceTemplate {
			ok = true
			m = dynaml.NewTemplateMarker(val)
		}
	}
	return m, ok
}
