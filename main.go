package main

import (
	"encoding/json"
	"fmt"
	"go/format"
	"go/scanner"
	"io/ioutil"
	"log"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/danielgtaylor/openapi-cli-generator/shorthand"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v2"
)

//go:generate go-bindata ./templates/...

// OpenAPI Extensions
const (
	ExtAliases     = "x-cli-aliases"
	ExtDescription = "x-cli-description"
	ExtIgnore      = "x-cli-ignore"
	ExtHidden      = "x-cli-hidden"
	ExtName        = "x-cli-name"
	ExtWaiters     = "x-cli-waiters"
	ExtXCli        = "x-cli"
)

// Param describes an OpenAPI parameter (path, query, header, etc)
type Param struct {
	Name        string
	CLIName     string
	GoName      string
	Description string
	In          string
	Required    bool
	Type        string
	TypeNil     string
	Style       string
	Explode     bool
	Redeclare   bool
}

// TagGroup describes a group of operations sharing the same tag
type TagGroup struct {
	Name        string
	Description string
	Path        string
	ParentPath  string
}

// XCli describes the x-cli extension for advanced CLI generation
type XCli struct {
	Domain   string          `json:"domain"`
	Resource string          `json:"resource"`
	Action   json.RawMessage `json:"action"`
	Verb     string          `json:"verb"`
	Hidden   bool            `json:"hidden"`
}

func (x *XCli) GetAction() string {
	if x.Action == nil {
		return ""
	}
	var s string
	if err := json.Unmarshal(x.Action, &s); err == nil {
		return s
	}
	var b bool
	if err := json.Unmarshal(x.Action, &b); err == nil {
		return fmt.Sprintf("%v", b)
	}
	return string(x.Action)
}

// Operation describes an OpenAPI operation (GET/POST/PUT/PATCH/DELETE)
type Operation struct {
	HandlerName    string
	GoName         string
	Use            string
	Aliases        []string
	Short          string
	Long           string
	Method         string
	CanHaveBody    bool
	ReturnType     string
	Path           string
	AllParams      []*Param
	RequiredParams []*Param
	OptionalParams []*Param
	MediaType      string
	Examples       []string
	Hidden         bool
	NeedsResponse  bool
	Waiters        []*WaiterParams
	Tag            string
}

// Waiter describes a special command that blocks until a condition has been
// met, after which it exits.
type Waiter struct {
	CLIName     string
	GoName      string
	Use         string
	Aliases     []string
	Short       string
	Long        string
	Delay       int
	Attempts    int
	OperationID string `json:"operationId"`
	Operation   *Operation
	Matchers    []*Matcher
	After       map[string]map[string]string
}

// Matcher describes a condition to match for a waiter.
type Matcher struct {
	Select   string
	Test     string
	Expected json.RawMessage
	State    string
}

// WaiterParams links a waiter with param selector querires to perform wait
// operations after a command has run.
type WaiterParams struct {
	Waiter *Waiter
	Args   []string
	Params map[string]string
}

// Server describes an OpenAPI server endpoint
type Server struct {
	Description string
	URL         string
	// TODO: handle server parameters
}

// Imports describe optional imports based on features in use.
type Imports struct {
	Fmt     bool
	Strings bool
	Time    bool
}

// OpenAPI describes an API
type OpenAPI struct {
	Imports      Imports
	Name         string
	GoName       string
	PublicGoName string
	Title        string
	Description  string
	Servers      []*Server
	Operations   []*Operation
	Waiters      []*Waiter
	TagGroups    []*TagGroup
	EnableXCliDravh bool
}

type PathPattern struct {
	PathRegex *regexp.Regexp
	Methods   map[string]bool
	RawLine   string
}

type PathFilter struct {
	Patterns []PathPattern
	IsAllow  bool
}

func isHTTPMethod(s string) bool {
	switch strings.ToUpper(s) {
	case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS", "TRACE", "CONNECT":
		return true
	}
	return false
}

func pathPatternToRegex(pattern string) string {
	var regexStr strings.Builder
	i := 0
	for i < len(pattern) {
		ch := pattern[i]
		if ch == '[' {
			j := strings.IndexByte(pattern[i+1:], ']')
			if j >= 0 {
				regexStr.WriteString(`\{[^/]+\}`)
				i += j + 2
				continue
			}
			regexStr.WriteString(`\[`)
			i++
			continue
		}
		if ch == ']' {
			regexStr.WriteString(`\]`)
			i++
			continue
		}
		if ch == '*' {
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				regexStr.WriteString(`.+`)
				i += 2
				continue
			}
			regexStr.WriteString(`[^/]+`)
			i++
			continue
		}
		if ch == '.' || ch == '+' || ch == '?' || ch == '(' || ch == ')' ||
			ch == '|' || ch == '{' || ch == '}' || ch == '^' || ch == '$' || ch == '\\' {
			regexStr.WriteByte('\\')
		}
		regexStr.WriteByte(ch)
		i++
	}
	return "^" + regexStr.String() + "$"
}

func parsePathPattern(line string) (PathPattern, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return PathPattern{}, false
	}

	var pathPart, methodPart string
	if idx := strings.LastIndex(line, ":"); idx >= 0 {
		potentialMethods := line[idx+1:]
		parts := strings.Split(potentialMethods, "|")
		allMethods := len(parts) > 0
		for _, p := range parts {
			if !isHTTPMethod(strings.TrimSpace(p)) {
				allMethods = false
				break
			}
		}
		if allMethods {
			pathPart = line[:idx]
			methodPart = potentialMethods
		} else {
			pathPart = line
		}
	} else {
		pathPart = line
	}

	regexStr := pathPatternToRegex(pathPart)

	var methods map[string]bool
	if methodPart != "" {
		methods = make(map[string]bool)
		for _, m := range strings.Split(methodPart, "|") {
			methods[strings.ToUpper(strings.TrimSpace(m))] = true
		}
	}

	return PathPattern{
		PathRegex: regexp.MustCompile(regexStr),
		Methods:   methods,
		RawLine:   line,
	}, true
}

func loadPathFilter(filename string, isAllow bool) *PathFilter {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		log.Fatalf("Failed to read filter file %s: %v", filename, err)
	}

	filter := &PathFilter{IsAllow: isAllow}
	for _, line := range strings.Split(string(data), "\n") {
		if pattern, ok := parsePathPattern(line); ok {
			filter.Patterns = append(filter.Patterns, pattern)
		}
	}

	if len(filter.Patterns) == 0 {
		log.Fatalf("No valid patterns found in filter file %s", filename)
	}

	return filter
}

func (f *PathFilter) IsAllowed(path string, method string) bool {
	matched := false
	for _, pattern := range f.Patterns {
		if pattern.PathRegex.MatchString(path) {
			if pattern.Methods == nil {
				matched = true
				break
			}
			if pattern.Methods[strings.ToUpper(method)] {
				matched = true
				break
			}
		}
	}

	if f.IsAllow {
		return matched
	}
	return !matched
}

// ProcessAPI returns the API description to be used with the commands template
// for a loaded and dereferenced OpenAPI 3 document.
func ProcessAPI(shortName string, api *openapi3.Swagger, rawData []byte, enableXCliDravh bool, pathFilter *PathFilter) *OpenAPI {
	apiName := shortName
	if api.Info.Extensions[ExtName] != nil {
		apiName = extStr(api.Info.Extensions[ExtName])
	}

	apiDescription := api.Info.Description
	if api.Info.Extensions[ExtDescription] != nil {
		apiDescription = extStr(api.Info.Extensions[ExtDescription])
	}

	result := &OpenAPI{
		Name:           apiName,
		GoName:         toGoName(shortName, false),
		PublicGoName:   toGoName(shortName, true),
		Title:          escapeString(api.Info.Title),
		Description:    escapeString(apiDescription),
		EnableXCliDravh: enableXCliDravh,
	}

	tagDescriptionMap := make(map[string]string)
	var topLevelTags []struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal(rawData, &struct {
		Tags *[]struct {
			Name        string `yaml:"name"`
			Description string `yaml:"description"`
		} `yaml:"tags"`
	}{
		Tags: &topLevelTags,
	}); err == nil {
		for _, t := range topLevelTags {
			tagDescriptionMap[t.Name] = t.Description
		}
	}

	for _, s := range api.Servers {
		result.Servers = append(result.Servers, &Server{
			Description: escapeString(s.Description),
			URL:         s.URL,
		})
	}

	// Convenience map for operation ID -> operation
	operationMap := make(map[string]*Operation)
	goNameCount := make(map[string]int)

	var keys []string
	for path := range api.Paths {
		keys = append(keys, path)
	}
	sort.Strings(keys)

	for _, path := range keys {
		item := api.Paths[path]

		if item.Extensions[ExtIgnore] != nil {
			// Ignore this path.
			continue
		}

		pathHidden := false
		if item.Extensions[ExtHidden] != nil {
			json.Unmarshal(item.Extensions[ExtHidden].(json.RawMessage), &pathHidden)
		}

		for method, operation := range item.Operations() {
			if operation.Extensions[ExtIgnore] != nil {
				continue
			}

			if pathFilter != nil && !pathFilter.IsAllowed(path, method) {
				continue
			}

			var xCli *XCli
			if operation.Extensions[ExtXCli] != nil {
				xCli = &XCli{}
				if err := json.Unmarshal(operation.Extensions[ExtXCli].(json.RawMessage), xCli); err != nil {
					panic(fmt.Errorf("failed to parse x-cli extension: %v", err))
				}
			}

			if result.EnableXCliDravh {
				if xCli == nil {
					continue
				}
				if xCli.Hidden {
					continue
				}
			}

			name := operation.OperationID
			if operation.Extensions[ExtName] != nil {
				name = extStr(operation.Extensions[ExtName])
			}

			var aliases []string
			if operation.Extensions[ExtAliases] != nil {
				// We need to decode the raw extension value into our string slice.
				json.Unmarshal(operation.Extensions[ExtAliases].(json.RawMessage), &aliases)
			}

			params := getParams(item, method)
			requiredParams := getRequiredParams(params)
			optionalParams := getOptionalParams(params)
			short := operation.Summary
			if short == "" {
				short = name
			}

			use := usage(name, requiredParams)

			description := operation.Description
			if operation.Extensions[ExtDescription] != nil {
				description = extStr(operation.Extensions[ExtDescription])
			}

			reqMt, reqSchema, reqExamples := getRequestInfo(operation)

			var examples []string
			if len(reqExamples) > 0 {
				wroteHeader := false
				for _, ex := range reqExamples {
					if _, ok := ex.(string); !ok {
						// Not a string, so it's structured data. Let's marshal it to the
						// shorthand syntax if we can.
						if m, ok := ex.(map[string]interface{}); ok {
							ex = shorthand.Get(m)
							examples = append(examples, ex.(string))
							continue
						}

						b, _ := json.Marshal(ex)

						if !wroteHeader {
							description += "\n## Input Example\n\n"
							wroteHeader = true
						}

						description += "\n" + string(b) + "\n"
						continue
					}

					if !wroteHeader {
						description += "\n## Input Example\n\n"
						wroteHeader = true
					}

					description += "\n" + ex.(string) + "\n"
				}
			}

			if reqSchema != "" {
				description += "\n## Request Schema (" + reqMt + ")\n\n" + reqSchema
			}

			method := strings.Title(strings.ToLower(method))
			if method == "Options" {
				method = "Head"
			}

			hidden := pathHidden
			if operation.Extensions[ExtHidden] != nil {
				json.Unmarshal(operation.Extensions[ExtHidden].(json.RawMessage), &hidden)
			}

			returnType := "interface{}"
		returnTypeLoop:
			for code, ref := range operation.Responses {
				if num, err := strconv.Atoi(code); err != nil || num < 200 || num >= 300 {
					// Skip invalid responses
					continue
				}

				if ref.Value != nil {
					for _, content := range ref.Value.Content {
						if _, ok := content.Example.(map[string]interface{}); ok {
							returnType = "map[string]interface{}"
							break returnTypeLoop
						}

						if content.Schema != nil && content.Schema.Value != nil {
							if content.Schema.Value.Type == "object" || len(content.Schema.Value.Properties) != 0 {
								returnType = "map[string]interface{}"
								break returnTypeLoop
							}
						}
					}
				}
			}

			opTag := ""
			goNameInput := name
			if result.EnableXCliDravh && xCli != nil {
				if action := xCli.GetAction(); action != "" {
					name = action
				}
				use = slug(name)
				for _, p := range requiredParams {
					use += " " + slug(p.Name)
				}
				resourceSegments := []string{}
				if xCli.Resource != "" {
					resourceSegments = strings.Split(strings.Trim(xCli.Resource, "/"), "/")
				}
				pathParts := []string{xCli.Domain}
				pathParts = append(pathParts, resourceSegments...)
				opTag = strings.Join(pathParts, "/")
				goNameInput = xCli.Domain
				for _, seg := range resourceSegments {
					goNameInput += "-" + seg
				}
				goNameInput += "-" + name
			} else {
				if len(operation.Tags) > 0 {
					opTag = operation.Tags[0]
				}
			}

			goName := toGoName(goNameInput, true)
			goNameCount[goName]++
			if goNameCount[goName] > 1 {
				goName = fmt.Sprintf("%s%d", goName, goNameCount[goName])
			}

			o := &Operation{
				HandlerName:    slug(name),
				GoName:         goName,
				Use:            use,
				Aliases:        aliases,
				Short:          escapeString(short),
				Long:           escapeString(description),
				Method:         method,
				CanHaveBody:    method == "Post" || method == "Put" || method == "Patch",
				ReturnType:     returnType,
				Path:           path,
				AllParams:      params,
				RequiredParams: requiredParams,
				OptionalParams: optionalParams,
				MediaType:      reqMt,
				Examples:       examples,
				Hidden:         hidden,
				Tag:            opTag,
			}

			requiredGoNames := make(map[string]bool)
			for _, p := range requiredParams {
				requiredGoNames[p.GoName] = true
			}
			for _, p := range optionalParams {
				if requiredGoNames[p.GoName] {
					p.Redeclare = true
				}
			}

			operationMap[operation.OperationID] = o

			result.Operations = append(result.Operations, o)

			for _, p := range params {
				if p.In == "path" {
					result.Imports.Strings = true
				}
			}

			for _, p := range optionalParams {
				if p.In == "query" || p.In == "header" {
					result.Imports.Fmt = true
				}
			}
		}
	}

	tagSet := make(map[string]bool)
	for _, op := range result.Operations {
		if op.Tag != "" {
			tagSet[op.Tag] = true
		}
	}

	if result.EnableXCliDravh {
		tagGroupSet := make(map[string]bool)
		for tagPath := range tagSet {
			segments := strings.Split(tagPath, "/")
			for i := 1; i <= len(segments); i++ {
				parentPath := strings.Join(segments[:i], "/")
				if !tagGroupSet[parentPath] {
					tagGroupSet[parentPath] = true
					parentPathForGroup := ""
					if i > 1 {
						parentPathForGroup = strings.Join(segments[:i-1], "/")
					}
					result.TagGroups = append(result.TagGroups, &TagGroup{
						Name:        segments[i-1],
						Description: "",
						Path:        parentPath,
						ParentPath:  parentPathForGroup,
					})
				}
			}
		}
		sort.Slice(result.TagGroups, func(i, j int) bool {
			return result.TagGroups[i].Path < result.TagGroups[j].Path
		})
	} else {
		for tagName := range tagSet {
			result.TagGroups = append(result.TagGroups, &TagGroup{
				Name:        tagName,
				Description: escapeString(tagDescriptionMap[tagName]),
				Path:        tagName,
				ParentPath:  "",
			})
		}
		sort.Slice(result.TagGroups, func(i, j int) bool {
			return result.TagGroups[i].Name < result.TagGroups[j].Name
		})
	}

	if api.Extensions[ExtWaiters] != nil {
		var waiters map[string]*Waiter

		if err := json.Unmarshal(api.Extensions[ExtWaiters].(json.RawMessage), &waiters); err != nil {
			panic(err)
		}

		for name, waiter := range waiters {
			waiter.CLIName = slug(name)
			waiter.GoName = toGoName(name+"-waiter", true)
			waiter.Operation = operationMap[waiter.OperationID]
			waiter.Use = usage(name, waiter.Operation.RequiredParams)
			waiter.Short = escapeString(waiter.Short)
			waiter.Long = escapeString(waiter.Long)

			for _, matcher := range waiter.Matchers {
				if matcher.Test == "" {
					matcher.Test = "equal"
				}
			}

			for operationID, waitOpParams := range waiter.After {
				op := operationMap[operationID]
				if op == nil {
					panic(fmt.Errorf("Unknown waiter operation %s", operationID))
				}

				var args []string
				for _, p := range op.RequiredParams {
					selector := waitOpParams[p.Name]
					if selector == "" {
						panic(fmt.Errorf("Missing required parameter %s", p.Name))
					}
					delete(waitOpParams, p.Name)

					args = append(args, selector)

					result.Imports.Fmt = true
					op.NeedsResponse = true
				}

				// Transform from OpenAPI param names to CLI names
				wParams := make(map[string]string)
				for p, s := range waitOpParams {
					found := false
					for _, optional := range op.OptionalParams {
						if optional.Name == p {
							wParams[optional.CLIName] = s
							found = true
							break
						}
					}
					if !found {
						panic(fmt.Errorf("Unknown parameter %s for waiter %s", p, name))
					}
				}

				op.Waiters = append(op.Waiters, &WaiterParams{
					Waiter: waiter,
					Args:   args,
					Params: wParams,
				})
			}

			result.Waiters = append(result.Waiters, waiter)
		}

		if len(waiters) > 0 {
			result.Imports.Time = true
		}
	}

	return result
}

// extStr returns the string value of an OpenAPI extension stored as a JSON
// raw message.
func extStr(i interface{}) (decoded string) {
	if err := json.Unmarshal(i.(json.RawMessage), &decoded); err != nil {
		panic(err)
	}

	return
}

func toGoName(input string, public bool) string {
	transformed := strings.Replace(input, "-", " ", -1)
	transformed = strings.Replace(transformed, "_", " ", -1)
	transformed = strings.Title(transformed)
	transformed = strings.Replace(transformed, " ", "", -1)

	if !public {
		transformed = strings.ToLower(string(transformed[0])) + transformed[1:]
	}

	return transformed
}

func escapeString(value string) string {
	transformed := strings.Replace(value, "\\", "\\\\", -1)
	transformed = strings.Replace(transformed, "\n", "\\n", -1)
	transformed = strings.Replace(transformed, "\"", "\\\"", -1)
	return transformed
}

func buildYAMLContext(lines []string, targetLine int) (int, string) {
	start := targetLine - 3
	if start < 0 {
		start = 0
	}
	end := targetLine + 3
	if end >= len(lines) {
		end = len(lines) - 1
	}
	var b strings.Builder
	for j := start; j <= end; j++ {
		marker := "  "
		if j == targetLine {
			marker = ">>"
		}
		fmt.Fprintf(&b, "%s %4d: %s\n", marker, j+1, lines[j])
	}
	return targetLine + 1, b.String()
}

var yamlBooleanValues = []string{
	"true", "True", "TRUE",
	"false", "False", "FALSE",
	"yes", "Yes", "YES",
	"no", "No", "NO",
	"on", "On", "ON",
	"off", "Off", "OFF",
	"y", "Y", "n", "N",
}

func findYAMLProblemLine(rawData []byte, errMsg string) (int, string) {
	re := regexp.MustCompile(`property '([^']+)'`)
	matches := re.FindAllStringSubmatch(errMsg, -1)
	var properties []string
	for _, m := range matches {
		if len(m) > 1 {
			properties = append(properties, m[1])
		}
	}

	isBoolMismatch := strings.Contains(errMsg, "cannot unmarshal bool")
	lines := strings.Split(string(rawData), "\n")

	for i := len(properties) - 1; i >= 0; i-- {
		propName := properties[i]
		for lineNum, line := range lines {
			trimmed := strings.TrimSpace(line)

			if isBoolMismatch {
				for _, bv := range yamlBooleanValues {
					if trimmed == propName+": "+bv || trimmed == propName+":"+bv {
						return buildYAMLContext(lines, lineNum)
					}
				}
			} else {
				if strings.Contains(line, propName) {
					return buildYAMLContext(lines, lineNum)
				}
			}
		}
	}

	return -1, ""
}

func stripNumericSuffix(operationID string) string {
	re := regexp.MustCompile(`_\d+$`)
	return re.ReplaceAllString(operationID, "")
}

func slug(operationID string) string {
	transformed := stripNumericSuffix(operationID)
	transformed = strings.Replace(transformed, "_", "-", -1)
	transformed = strings.Replace(transformed, " ", "-", -1)
	return transformed
}

func usage(name string, requiredParams []*Param) string {
	usage := slug(name)

	for _, p := range requiredParams {
		usage += " " + slug(p.Name)
	}

	return usage
}

func getParams(path *openapi3.PathItem, httpMethod string) []*Param {
	operation := path.Operations()[httpMethod]
	allParams := make([]*Param, 0, len(path.Parameters))

	var total openapi3.Parameters
	total = append(total, path.Parameters...)
	total = append(total, operation.Parameters...)

	for _, p := range total {
		if p.Value != nil && p.Value.Extensions["x-cli-ignore"] == nil {
			t := "string"
			tn := "\"\""
			if p.Value.Schema != nil && p.Value.Schema.Value != nil && p.Value.Schema.Value.Type != "" {
				switch p.Value.Schema.Value.Type {
				case "boolean":
					t = "bool"
					tn = "false"
				case "integer":
					t = "int64"
					tn = "0"
				case "number":
					t = "float64"
					tn = "0.0"
				}
			}

			cliName := slug(p.Value.Name)
			if p.Value.Extensions[ExtName] != nil {
				cliName = extStr(p.Value.Extensions[ExtName])
			}

			description := p.Value.Description
			if p.Value.Extensions[ExtDescription] != nil {
				description = extStr(p.Value.Extensions[ExtDescription])
			}

			allParams = append(allParams, &Param{
				Name:        p.Value.Name,
				CLIName:     cliName,
				GoName:      toGoName("param "+cliName, false),
				Description: escapeString(description),
				In:          p.Value.In,
				Required:    p.Value.Required,
				Type:        t,
				TypeNil:     tn,
			})
		}
	}

	return allParams
}

func getRequiredParams(allParams []*Param) []*Param {
	required := make([]*Param, 0)

	for _, param := range allParams {
		if param.In == "path" {
			required = append(required, param)
		}
	}

	return required
}

func getOptionalParams(allParams []*Param) []*Param {
	optional := make([]*Param, 0)

	for _, param := range allParams {
		if param.In != "path" {
			optional = append(optional, param)
		}
	}

	return optional
}

func getRequestInfo(op *openapi3.Operation) (string, string, []interface{}) {
	mts := make(map[string][]interface{})

	if op.RequestBody != nil && op.RequestBody.Value != nil {
		for mt, item := range op.RequestBody.Value.Content {
			var schema string
			var examples []interface{}

			if item.Schema != nil && item.Schema.Value != nil {
				// Let's make this a bit more concise. Since it has special JSON
				// marshalling functions, we do a dance to get it into plain JSON before
				// converting to YAML.
				data, err := json.Marshal(item.Schema.Value)
				if err != nil {
					continue
				}

				var unmarshalled interface{}
				json.Unmarshal(data, &unmarshalled)

				data, err = yaml.Marshal(unmarshalled)
				if err == nil {
					schema = string(data)
				}
			}

			if item.Example != nil {
				examples = append(examples, item.Example)
			} else {
				for _, ex := range item.Examples {
					if ex.Value != nil {
						examples = append(examples, ex.Value.Value)
						break
					}
				}
			}

			mts[mt] = []interface{}{schema, examples}
		}
	}

	// Prefer JSON.
	for mt, item := range mts {
		if strings.Contains(mt, "json") {
			return mt, item[0].(string), item[1].([]interface{})
		}
	}

	// Fall back to YAML next.
	for mt, item := range mts {
		if strings.Contains(mt, "yaml") {
			return mt, item[0].(string), item[1].([]interface{})
		}
	}

	// Last resort: return the first we find!
	for mt, item := range mts {
		return mt, item[0].(string), item[1].([]interface{})
	}

	return "", "", nil
}

func writeFormattedFile(filename string, data []byte) {
	formatted, errFormat := format.Source(data)
	if errFormat != nil {
		formatted = data
	}

	err := ioutil.WriteFile(filename, formatted, 0600)
	if errFormat != nil {
		var errMsg strings.Builder
		fmt.Fprintf(&errMsg, "Failed to format generated Go file %s:\n  %v\n", filename, errFormat)

		goLines := strings.Split(string(data), "\n")
		if errList, ok := errFormat.(scanner.ErrorList); ok && len(errList) > 0 {
			firstErr := errList[0]
			ln := firstErr.Pos.Line
			if ln > 0 && ln <= len(goLines) {
				start := ln - 3
				if start < 1 {
					start = 1
				}
				end := ln + 3
				if end > len(goLines) {
					end = len(goLines)
				}
				fmt.Fprintf(&errMsg, "\nGenerated Go file context around line %d:\n", ln)
				for j := start; j <= end; j++ {
					marker := "  "
					if j == ln {
						marker = ">>"
					}
					fmt.Fprintf(&errMsg, "%s %5d: %s\n", marker, j, goLines[j-1])
				}
			}
		}

		panic(errMsg.String())
	} else if err != nil {
		panic(err)
	}
}

func initCmd(cmd *cobra.Command, args []string) {
	if _, err := os.Stat("main.go"); err == nil {
		fmt.Println("Refusing to overwrite existing main.go")
		return
	}

	data, _ := Asset("templates/main.tmpl")
	tmpl, err := template.New("cli").Parse(string(data))
	if err != nil {
		panic(err)
	}

	templateData := map[string]string{
		"Name":    args[0],
		"NameEnv": strings.Replace(strings.ToUpper(args[0]), "-", "_", -1),
	}

	var sb strings.Builder
	err = tmpl.Execute(&sb, templateData)
	if err != nil {
		panic(err)
	}

	writeFormattedFile("main.go", []byte(sb.String()))
}

func generate(cmd *cobra.Command, args []string) {
	enableXCliDravh, _ := cmd.Flags().GetBool("x-cli-dravh")
	allowListFile, _ := cmd.Flags().GetString("allow-list")
	disallowListFile, _ := cmd.Flags().GetString("disallow-list")

	if allowListFile != "" && disallowListFile != "" {
		log.Fatal("--allow-list and --disallow-list are mutually exclusive")
	}

	var pathFilter *PathFilter
	if allowListFile != "" {
		pathFilter = loadPathFilter(allowListFile, true)
	} else if disallowListFile != "" {
		pathFilter = loadPathFilter(disallowListFile, false)
	}

	data, err := ioutil.ReadFile(args[0])
	if err != nil {
		log.Fatal(err)
	}

	rawData := data

	// Load the OpenAPI document.
	loader := openapi3.NewSwaggerLoader()
	var swagger *openapi3.Swagger
	swagger, err = loader.LoadSwaggerFromData(data)
	if err != nil {
		var errMsg strings.Builder
		fmt.Fprintf(&errMsg, "Failed to load OpenAPI document from %s:\n  %v\n", args[0], err)

		yamlLine, yamlCtx := findYAMLProblemLine(rawData, err.Error())
		if yamlLine > 0 {
			fmt.Fprintf(&errMsg, "\nProblem seems related to YAML file %s line %d:\n", args[0], yamlLine)
			errMsg.WriteString(yamlCtx)
		}

		log.Fatal(errMsg.String())
	}

	funcs := template.FuncMap{
		"escapeStr": escapeString,
		"slug":      slug,
		"title":     strings.Title,
	}

	data, _ = Asset("templates/commands.tmpl")
	tmpl, err := template.New("cli").Funcs(funcs).Parse(string(data))
	if err != nil {
		panic(err)
	}

	shortName := strings.TrimSuffix(path.Base(args[0]), ".yaml")

	templateData := ProcessAPI(shortName, swagger, rawData, enableXCliDravh, pathFilter)

	var sb strings.Builder
	err = tmpl.Execute(&sb, templateData)
	if err != nil {
		panic(err)
	}

	writeFormattedFile(shortName+".go", []byte(sb.String()))
}

func main() {
	root := &cobra.Command{}

	root.AddCommand(&cobra.Command{
		Use:   "init <app-name>",
		Short: "Initialize and generate a `main.go` file for your project",
		Args:  cobra.ExactArgs(1),
		Run:   initCmd,
	})

	genCmd := &cobra.Command{
		Use:   "generate <api-spec>",
		Short: "Generate a `commands.go` file from an OpenAPI spec",
		Args:  cobra.ExactArgs(1),
		Run:   generate,
	}
	genCmd.Flags().Bool("x-cli-dravh", false, "Enable x-cli driven command generation (filter by x-cli, use domain/resource/action for command hierarchy)")
	genCmd.Flags().String("allow-list", "", "File containing allowed API paths (whitelist, mutually exclusive with --disallow-list)")
	genCmd.Flags().String("disallow-list", "", "File containing disallowed API paths (blacklist, mutually exclusive with --allow-list)")
	root.AddCommand(genCmd)

	root.Execute()
}
