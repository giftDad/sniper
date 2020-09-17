// Copyright 2018 Twitch Interactive, Inc.  All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may not
// use this file except in compliance with the License. A copy of the License is
// located at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// or in the "license" file accompanying this file. This file is distributed on
// an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"fmt"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"log"
	"path"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"sniper/cmd/protoc-gen-twirp/templates"
	"sniper/cmd/protoc-gen-twirp/templates/rule"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

const Version = "v0.1.0"

type twirp struct {
	// OptionPrefix method_option flag
	OptionPrefix string
	// TwirpPackage twirp 运行库包名
	// 默认为 sniper/util/twirp，用户可以使用 twirp_package 定制
	// 如果修改过项目的默认包名(sniper)则一定需要指定
	TwirpPackage string
	// 是否开启 validate
	ValidateEnable bool

	filesHandled int

	// Map to record whether we've built each package
	pkgs          map[string]string
	pkgNamesInUse map[string]bool
	deps          map[string]string

	methodOptionRegexp *regexp.Regexp

	plugin *protogen.Plugin

	// Output buffer that holds the bytes we want to write out for a single file.
	// Gets reset after working on a file.
	output *bytes.Buffer
}

func getFieldType(k protoreflect.Kind) (string, string) {
	switch k {
	case protoreflect.StringKind:
		return "string", ""
	case protoreflect.DoubleKind:
		return "float", "64"
	case protoreflect.FloatKind:
		return "float", "32"
	case protoreflect.Int32Kind:
		return "int", "32"
	case protoreflect.Int64Kind:
		return "int", "64"
	case protoreflect.Uint32Kind:
		return "uint", "32"
	case protoreflect.Uint64Kind:
		return "uint", "64"
	case protoreflect.BoolKind:
		return "bool", ""
	default:
		return "", ""
	}
}

func newGenerator() *twirp {
	t := &twirp{
		pkgs:          make(map[string]string),
		pkgNamesInUse: make(map[string]bool),
		deps:          make(map[string]string),
		output:        bytes.NewBuffer(nil),
	}

	return t
}

func (t *twirp) Generate(plugin *protogen.Plugin) error {
	t.plugin = plugin

	t.methodOptionRegexp = regexp.MustCompile(t.OptionPrefix + `:([^:\s]+)`)

	// Register names of packages that we import.
	t.registerPackageName("bytes")
	t.registerPackageName("strings")
	t.registerPackageName("context")
	t.registerPackageName("http")
	t.registerPackageName("io")
	t.registerPackageName("ioutil")
	t.registerPackageName("json")
	t.registerPackageName("jsonpb")
	t.registerPackageName("proto")
	t.registerPackageName("twirp")
	t.registerPackageName("url")
	t.registerPackageName("fmt")
	t.registerPackageName("errors")
	t.registerPackageName("strconv")
	t.registerPackageName("ctxkit")

	for _, f := range t.plugin.Files {
		if len(f.Services) == 0 {
			continue
		}

		t.generate(f)
		if t.ValidateEnable {
			t.generateValidate(f)
		}
		t.filesHandled++
	}

	return nil
}

func (t *twirp) registerPackageName(name string) (alias string) {
	alias = name
	i := 1
	for t.pkgNamesInUse[alias] {
		alias = name + strconv.Itoa(i)
		i++
	}
	t.pkgNamesInUse[alias] = true
	t.pkgs[name] = alias
	return alias
}

func (t *twirp) generate(file *protogen.File) {
	t.generateFileHeader(file)

	t.generateImports(file)

	for i, service := range file.Services {
		t.generateService(file, service, i)
	}

	t.generateFileDescriptor(file)

	fname := file.GeneratedFilenamePrefix + ".twirp.go"
	gf := t.plugin.NewGeneratedFile(fname, file.GoImportPath)
	gf.Write(t.formattedOutput(t.output.Bytes()))
	t.output.Reset()
}

func (t *twirp) generateValidate(file *protogen.File) {
	fname := file.GeneratedFilenamePrefix + ".validate.go"

	tpl := template.New(fname)
	rule.RegisterFunctions(tpl)
	templates.Register(tpl)

	buf := &bytes.Buffer{}
	if err := tpl.Execute(buf, file); err != nil {
		panic(err)
	}

	gf := t.plugin.NewGeneratedFile(fname, file.GoImportPath)
	gf.Write(t.formattedOutput(buf.Bytes()))
}

func (t *twirp) generateFileHeader(file *protogen.File) {
	t.P("// Package ", string(file.GoPackageName), " is generated by protoc-gen-twirp ", Version, ", DO NOT EDIT.")
	t.P("// source: ", file.Desc.Path())
	t.P(`package `, string(file.GoPackageName))
	t.P()
}

func (t *twirp) generateImports(file *protogen.File) {
	t.P(`import `, t.pkgs["bytes"], ` "bytes"`)
	t.P(`import `, t.pkgs["strings"], ` "strings"`)
	t.P(`import `, t.pkgs["context"], ` "context"`)
	t.P(`import `, t.pkgs["fmt"], ` "fmt"`)
	t.P(`import `, t.pkgs["strconv"], ` "strconv"`)
	t.P(`import `, t.pkgs["errors"], ` "errors"`)
	t.P(`import `, t.pkgs["ioutil"], ` "io/ioutil"`)
	t.P(`import `, t.pkgs["http"], ` "net/http"`)
	t.P()
	t.P(`import `, t.pkgs["jsonpb"], ` "github.com/golang/protobuf/jsonpb"`)
	t.P(`import `, t.pkgs["proto"], ` "github.com/golang/protobuf/proto"`)
	t.P(`import `, t.pkgs["ctxkit"], ` "sniper/util/ctxkit"`)
	t.P(`import `, t.pkgs["twirp"], fmt.Sprintf(` "%s"`, t.TwirpPackage))
	t.P()

	// It's legal to import a message and use it as an input or output for a
	// method. Make sure to import the package of any such message. First, dedupe
	// them.
	for _, s := range file.Services {
		for _, m := range s.Methods {
			defs := []*protogen.Message{m.Input, m.Output}
			for _, def := range defs {
				if def.GoIdent.GoImportPath == file.GoImportPath {
					continue
				}
				p := string(def.GoIdent.GoImportPath)
				pkg := path.Base(p)
				t.deps[pkg] = strconv.Quote(p)

			}
		}
	}
	for pkg, importPath := range t.deps {
		t.P(`import `, pkg, ` `, importPath)
	}
	if len(t.deps) > 0 {
		t.P()
	}

	t.P(`// If the request does not have any number filed, the strconv`)
	t.P(`// is not needed. However, there is no easy way to drop it.`)
	t.P(`var _ = `, t.pkgs["strconv"], `.IntSize`)
	t.P(`var _ = `, t.pkgs["ctxkit"], `.GetUserID`)
	t.P()
}

// P forwards to g.gen.P, which prints output.
func (t *twirp) P(args ...string) {
	for _, v := range args {
		t.output.WriteString(v)
	}
	t.output.WriteByte('\n')
}

// Big header comments to makes it easier to visually parse a generated file.
func (t *twirp) sectionComment(sectionTitle string) {
	t.P()
	t.P(`// `, strings.Repeat("=", len(sectionTitle)))
	t.P(`// `, sectionTitle)
	t.P(`// `, strings.Repeat("=", len(sectionTitle)))
	t.P()
}

func (t *twirp) generateService(file *protogen.File, service *protogen.Service, index int) {
	t.sectionComment(service.GoName + ` Interface`)
	t.generateTwirpInterface(file, service)

	t.sectionComment(service.GoName + ` Protobuf Client`)
	t.generateClient("Protobuf", file, service)

	t.sectionComment(service.GoName + ` JSON Client`)
	t.generateClient("JSON", file, service)

	t.sectionComment(service.GoName + ` Server Handler`)
	t.generateServer(file, service)
}

func (t *twirp) generateTwirpInterface(file *protogen.File, service *protogen.Service) {
	t.printComments(service.Comments)
	t.P(`type `, service.GoName, ` interface {`)
	for _, method := range service.Methods {
		t.printComments(method.Comments)
		t.P(t.generateSignature(method))
		t.P()
	}
	t.P(`}`)
}

func (t *twirp) generateSignature(method *protogen.Method) string {
	methName := method.GoName
	inputType := t.getType(method.Input)
	outputType := t.getType(method.Output)
	return fmt.Sprintf(`	%s(%s.Context, *%s) (*%s, error)`, methName, t.pkgs["context"], inputType, outputType)
}

func (t *twirp) getType(m *protogen.Message) string {
	pkg := path.Base(string(m.GoIdent.GoImportPath))
	if _, ok := t.deps[pkg]; ok {
		return pkg + "." + m.GoIdent.GoName
	}
	return m.GoIdent.GoName
}

// valid names: 'JSON', 'Protobuf'
func (t *twirp) generateClient(name string, file *protogen.File, service *protogen.Service) {
	servName := service.GoName
	pathPrefixConst := servName + "PathPrefix"
	structName := unexported(servName) + name + "Client"
	newClientFunc := "New" + servName + name + "Client"

	methCnt := strconv.Itoa(len(service.Methods))
	t.P(`type `, structName, ` struct {`)
	t.P(`  client `, t.pkgs["twirp"], `.HTTPClient`)
	t.P(`  urls   [`, methCnt, `]string`)
	t.P(`}`)
	t.P()
	t.P(`// `, newClientFunc, ` creates a `, name, ` client that implements the `, servName, ` interface.`)
	t.P(`// It communicates using `, name, ` and can be configured with a custom HTTPClient.`)
	t.P(`func `, newClientFunc, `(addr string, client `, t.pkgs["twirp"], `.HTTPClient) `, servName, ` {`)
	t.P(`  prefix := addr + `, pathPrefixConst)
	t.P(`  urls := [`, methCnt, `]string{`)
	for _, method := range service.Methods {
		t.P(`    	prefix + "`, method.GoName, `",`)
	}
	t.P(`  }`)
	t.P(`  return &`, structName, `{`)
	t.P(`    client: client,`)
	t.P(`    urls:   urls,`)
	t.P(`  }`)
	t.P(`}`)
	t.P()

	for i, method := range service.Methods {
		methName := method.GoName
		inputType := t.getType(method.Input)
		outputType := t.getType(method.Output)

		t.P(`func (c *`, structName, `) `, methName, `(ctx `, t.pkgs["context"], `.Context, in *`, inputType, `) (*`, outputType, `, error) {`)
		t.P(`  ctx = `, t.pkgs["twirp"], `.WithPackageName(ctx, "`, *file.Proto.Package, `")`)
		t.P(`  ctx = `, t.pkgs["twirp"], `.WithServiceName(ctx, "`, servName, `")`)
		t.P(`  ctx = `, t.pkgs["twirp"], `.WithMethodName(ctx, "`, methName, `")`)
		t.P(`  out := new(`, outputType, `)`)
		t.P(`  err := `, t.pkgs["twirp"], `.Do`, name, `Request(ctx, c.client, c.urls[`, strconv.Itoa(i), `], in, out)`)
		t.P(`  if err != nil {`)
		t.P(`    return nil, err`)
		t.P(`  }`)
		t.P(`  return out, nil`)
		t.P(`}`)
		t.P()
	}
}

func (t *twirp) generateServer(file *protogen.File, service *protogen.Service) {
	servName := service.GoName

	// Server implementation.
	servStruct := serviceStruct(service)
	t.P(`type `, servStruct, ` struct {`)
	t.P(`  `, servName)
	t.P(`  hooks     *`, t.pkgs["twirp"], `.ServerHooks`)
	t.P(`}`)
	t.P()

	// Constructor for server implementation
	t.P(`func New`, servName, `Server(svc `, servName, `, hooks *`, t.pkgs["twirp"], `.ServerHooks) `, t.pkgs["twirp"], `.Server {`)
	t.P(`  return &`, servStruct, `{`)
	t.P(`    `, servName, `: svc,`)
	t.P(`    hooks: hooks,`)
	t.P(`  }`)
	t.P(`}`)
	t.P()

	// Write Errors
	t.P(`// writeError writes an HTTP response with a valid Twirp error format, and triggers hooks.`)
	t.P(`// If err is not a twirp.Error, it will get wrapped with twirp.InternalErrorWith(err)`)
	t.P(`func (s *`, servStruct, `) writeError(ctx `, t.pkgs["context"], `.Context, resp `, t.pkgs["http"], `.ResponseWriter, err error) {`)
	t.P(`  s.hooks.WriteError(ctx, resp, err)`)
	t.P(`}`)
	t.P()

	// badRouteError
	t.P(`// badRouteError is used when the twirp server cannot route a request`)
	t.P(`func (s *`, servStruct, `) badRouteError(msg string, method, url string) `, t.pkgs["twirp"], `.Error {`)
	t.P(`	err := twirp.NewError(twirp.BadRoute, msg)`)
	t.P(`	err = err.WithMeta("twirp_invalid_route", method+" "+url)`)
	t.P(`	return err`)
	t.P(`}`)
	t.P()

	t.P(`func (s *`, servStruct, `) wrapErr(err error, msg string) error {`)
	t.P(`	return errors.New(msg + ": " + err.Error())`)
	t.P(`}`)

	// Routing.
	t.generateServerRouting(servStruct, file, service)

	// Methods.
	for _, method := range service.Methods {
		t.generateServerMethod(file, service, method)
	}

	t.generateServiceMetadataAccessors(file, service)
}

// pathPrefix returns the base path for all methods handled by a particular
// service. It includes a trailing slash. (for example
// "/twitch.example.Haberdasher/").
func (t *twirp) pathPrefix(service *protogen.Service) string {
	return "/" + string(service.Desc.FullName()) + "/"
}

// pathFor returns the complete path for requests to a particular method on a
// particular service.
func (t *twirp) pathFor(service *protogen.Service, method *protogen.Method) string {
	return t.pathPrefix(service) + method.GoName
}

func (t *twirp) generateServerRouting(servStruct string, file *protogen.File, service *protogen.Service) {
	servName := service.GoName

	pathPrefixConst := servName + "PathPrefix"
	t.P(`// `, pathPrefixConst, ` is used for all URL paths on a twirp `, servName, ` server.`)
	t.P(`// Requests are always: POST `, pathPrefixConst, `/method`)
	t.P(`// It can be used in an HTTP mux to route twirp requests along with non-twirp requests on other routes.`)
	t.P(`const `, pathPrefixConst, ` = `, strconv.Quote(t.pathPrefix(service)))
	t.P()

	t.P(`func (s *`, servStruct, `) ServeHTTP(resp `, t.pkgs["http"], `.ResponseWriter, req *`, t.pkgs["http"], `.Request) {`)
	t.P(`  ctx := req.Context()`)
	t.P(`  ctx = `, t.pkgs["twirp"], `.WithHttpRequest(ctx, req)`)
	t.P(`  ctx = `, t.pkgs["twirp"], `.WithPackageName(ctx, "`, *file.Proto.Package, `")`)
	t.P(`  ctx = `, t.pkgs["twirp"], `.WithServiceName(ctx, "`, servName, `")`)
	t.P(`  ctx = `, t.pkgs["twirp"], `.WithResponseWriter(ctx, resp)`)
	t.P()
	t.P(`  var err error`)
	t.P(`  ctx, err = s.hooks.CallRequestReceived(ctx)`)
	t.P(`  if err != nil {`)
	t.P(`    s.writeError(ctx, resp, err)`)
	t.P(`    return`)
	t.P(`  }`)
	t.P()
	t.P(`  if req.Method != "POST" && !`, t.pkgs["twirp"], `.AllowGET(ctx) {`)
	t.P(`    msg := `, t.pkgs["fmt"], `.Sprintf("unsupported method %q (only POST is allowed)", req.Method)`)
	t.P(`    err = s.badRouteError(msg, req.Method, req.URL.Path)`)
	t.P(`    s.writeError(ctx, resp, err)`)
	t.P(`    return`)
	t.P(`  }`)
	t.P()
	t.P(`  switch req.URL.Path {`)
	for _, method := range service.Methods {
		path := t.pathFor(service, method)
		methName := "serve" + method.GoName
		t.P(`  case `, strconv.Quote(path), `:`)
		t.P(`    s.`, methName, `(ctx, resp, req)`)
		t.P(`    return`)
	}
	t.P(`  default:`)
	t.P(`    msg := `, t.pkgs["fmt"], `.Sprintf("no handler for path %q", req.URL.Path)`)
	t.P(`    err = s.badRouteError(msg, req.Method, req.URL.Path)`)
	t.P(`    s.writeError(ctx, resp, err)`)
	t.P(`    return`)
	t.P(`  }`)
	t.P(`}`)
	t.P()
}

func (t *twirp) generateServerMethod(file *protogen.File, service *protogen.Service, method *protogen.Method) {
	methName := method.GoName
	servStruct := serviceStruct(service)
	t.P(`func (s *`, servStruct, `) serve`, methName, `(ctx `, t.pkgs["context"], `.Context, resp `, t.pkgs["http"], `.ResponseWriter, req *`, t.pkgs["http"], `.Request) {`)
	t.P(`  header := req.Header.Get("Content-Type")`)
	t.P(`  i := strings.Index(header, ";")`)
	t.P(`  if i == -1 {`)
	t.P(`    i = len(header)`)
	t.P(`  }`)

	matched := t.methodOptionRegexp.FindStringSubmatch(method.Comments.Trailing.String())
	if len(matched) == 2 {
		t.P(`  ctx = twirp.WithMethodOption(ctx, "`, matched[1], `")`)
	}

	t.P(`  switch strings.TrimSpace(strings.ToLower(header[:i])) {`)
	t.P(`  case "application/json":`)
	t.P(`    s.serve`, methName, `JSON(ctx, resp, req)`)
	t.P(`  case "application/protobuf":`)
	t.P(`    s.serve`, methName, `Protobuf(ctx, resp, req)`)
	t.P(`  default:`)
	t.P(`    s.serve`, methName, `Form(ctx, resp, req)`)
	t.P(`  }`)
	t.P(`}`)
	t.P()
	t.generateServerJSONMethod(service, method)
	t.generateServerProtobufMethod(service, method)
	t.generateServerFormMethod(service, method)
}

func (t *twirp) needLogin(method *protogen.Method, service *protogen.Service) bool {
	return strings.Contains(string(method.Comments.Leading), "@auth\n") || strings.Contains(string(service.Comments.Leading), "@auth\n")
}

func (t *twirp) generateServerJSONMethod(service *protogen.Service, method *protogen.Method) {
	servStruct := serviceStruct(service)
	methName := method.GoName
	servName := service.GoName
	t.P(`func (s *`, servStruct, `) serve`, methName, `JSON(ctx `, t.pkgs["context"], `.Context, resp `, t.pkgs["http"], `.ResponseWriter, req *`, t.pkgs["http"], `.Request) {`)
	t.P(`  var err error`)
	t.P(`  ctx = `, t.pkgs["twirp"], `.WithMethodName(ctx, "`, methName, `")`)
	t.P(`  ctx, err = s.hooks.CallRequestRouted(ctx)`)
	t.P(`  if err != nil {`)
	t.P(`    s.writeError(ctx, resp, err)`)
	t.P(`    return`)
	t.P(`  }`)
	t.P()
	t.P(`  reqContent := new(`, t.getType(method.Input), `)`)
	t.P(`  unmarshaler := `, t.pkgs["jsonpb"], `.Unmarshaler{AllowUnknownFields: true}`)
	t.P(`  if err = unmarshaler.Unmarshal(req.Body, reqContent); err != nil {`)
	t.P(`    err = s.wrapErr(err, "failed to parse request json")`)
	t.P(`    twerr := `, t.pkgs["twirp"], `.NewError(`, t.pkgs["twirp"], `.InvalidArgument, err.Error())`)
	t.P(`    twerr = twerr.WithMeta("cause", `, t.pkgs["fmt"], `.Sprintf("%T", err))`)
	t.P(`    s.writeError(ctx, resp, twerr)`)
	t.P(`    return`)
	t.P(`  }`)
	t.P()
	t.P(`  ctx = twirp.WithRequest(ctx, reqContent)`)
	t.addValidate(method, service)
	t.P(`  // Call service method`)
	t.P(`  var respContent *`, t.getType(method.Output))
	t.P(`  func() {`)
	t.P(`    defer func() {`)
	t.P(`      // In case of a panic, serve a 500 error and then panic.`)
	t.P(`      if r := recover(); r != nil {`)
	t.P(`        s.writeError(ctx, resp, `, t.pkgs["twirp"], `.InternalError("Internal service panic"))`)
	t.P(`        panic(r)`)
	t.P(`      }`)
	t.P(`    }()`)
	t.P(`    respContent, err = s.`, servName, `.`, methName, `(ctx, reqContent)`)
	t.P(`  }()`)
	t.P()
	t.P(`  if err != nil {`)
	t.P(`    s.writeError(ctx, resp, err)`)
	t.P(`    return`)
	t.P(`  }`)
	t.P(`  if respContent == nil {`)
	t.P(`    s.writeError(ctx, resp, `, t.pkgs["twirp"], `.InternalError("received a nil *`, t.getType(method.Output), ` and nil error while calling `, methName, `. nil responses are not supported"))`)
	t.P(`    return`)
	t.P(`  }`)
	t.P()
	t.P(`  ctx = twirp.WithResponse(ctx, respContent)`)
	t.P()
	t.P(`  ctx = s.hooks.CallResponsePrepared(ctx)`)
	t.P()
	t.P(`  type httpBody interface {`)
	t.P(`    GetContentType() string`)
	t.P(`    GetData() []byte`)
	t.P(`  }`)
	t.P()
	t.P(`  var respBytes []byte`)
	t.P(`  var respStatus = `, t.pkgs["http"], `.StatusOK`)
	t.P(`  if body, ok := interface{}(respContent).(httpBody); ok {`)
	t.P(`    type httpStatus interface{ GetStatus() int32 }`)
	t.P(`    if statusBody, ok := interface{}(respContent).(httpStatus); ok {`)
	t.P(`      if status := statusBody.GetStatus(); status > 0 {`)
	t.P(`        respStatus = int(status)`)
	t.P(`      }`)
	t.P(`    }`)
	t.P(`    if contentType := body.GetContentType(); contentType != "" {`)
	t.P(`      resp.Header().Set("Content-Type", contentType)`)
	t.P(`    }`)
	t.P(`    respBytes = body.GetData()`)
	t.P(`  } else {`)
	t.P(`    var buf `, t.pkgs["bytes"], `.Buffer`)
	t.P(`    marshaler := &`, t.pkgs["jsonpb"], `.Marshaler{OrigName: true, EmitDefaults: true }`)
	t.P(`    if err = marshaler.Marshal(&buf, respContent); err != nil {`)
	t.P(`      err = s.wrapErr(err, "failed to marshal json response")`)
	t.P(`      s.writeError(ctx, resp, `, t.pkgs["twirp"], `.InternalErrorWith(err))`)
	t.P(`      return`)
	t.P(`    }`)
	t.P(`    respBytes = buf.Bytes()`)
	t.P(`    resp.Header().Set("Content-Type", "application/json")`)
	t.P(`  }`)
	t.P()
	t.P(`  ctx = `, t.pkgs["twirp"], `.WithStatusCode(ctx, respStatus)`)
	t.P(`  resp.WriteHeader(respStatus)`)
	t.P()
	t.P(`  if n, err := resp.Write(respBytes); err != nil {`)
	t.P(`    msg := fmt.Sprintf("failed to write response, %d of %d bytes written: %s", n, len(respBytes), err.Error())`)
	t.P(`    twerr := `, t.pkgs["twirp"], `.NewError(`, t.pkgs["twirp"], `.Unknown, msg)`)
	t.P(`    s.hooks.CallError(ctx, twerr)`)
	t.P(`  }`)
	t.P(`  s.hooks.CallResponseSent(ctx)`)
	t.P(`}`)
	t.P()
}

func (t *twirp) generateServerFormMethod(service *protogen.Service, method *protogen.Method) {
	servStruct := serviceStruct(service)
	methName := method.GoName
	t.P(`func (s *`, servStruct, `) serve`, methName, `Form(ctx `, t.pkgs["context"], `.Context, resp `, t.pkgs["http"], `.ResponseWriter, req *`, t.pkgs["http"], `.Request) {`)
	t.P(`  var err error`)
	t.P(`  ctx = `, t.pkgs["twirp"], `.WithMethodName(ctx, "`, methName, `")`)
	t.P(`  ctx, err = s.hooks.CallRequestRouted(ctx)`)
	t.P(`  if err != nil {`)
	t.P(`    s.writeError(ctx, resp, err)`)
	t.P(`    return`)
	t.P(`  }`)
	t.P()
	t.P(`  err = req.ParseForm()`)
	t.P(`  if err != nil {`)
	t.P(`    s.writeError(ctx, resp, err)`)
	t.P(`    return`)
	t.P(`  }`)
	t.P()
	t.P(`  reqContent := new(`, t.getType(method.Input), `)`)
	t.P()
	t.addValidate(method, service)

	for _, field := range method.Input.Fields {
		ft, fs := getFieldType(field.Desc.Kind())

		if ft == "" {
			continue
		}

		t.P(`  if v, ok := req.Form["`, string(field.Desc.Name()), `"]; ok {`)
		if field.Desc.IsList() {
			t.P(`    if len(v) == 1 {`)
			t.P(`        v = strings.Split(v[0], ",")`)
			t.P(`    }`)
			if ft == "string" {
				t.P(`    reqContent.`, field.GoName, ` = v `)
			} else {
				t.P(`    vs := make([]`, ft, fs, `, 0, len(v))`)
				t.P(`    for _, vv := range(v) {`)
				if ft == "float" {
					t.P(`      vvv, err := strconv.ParseFloat(vv, `, fs, `)`)
				} else if ft == "bool" {
					t.P(`      vvv, err := strconv.ParseBool(vv)`)
				} else {
					t.P(`      vvv, err := strconv.Parse`, exported(ft), `(vv, 10, `, fs, `)`)
				}
				t.P(`      if err != nil {`)
				t.P(`        s.writeError(ctx, resp, twirp.InvalidArgumentError("`, string(field.Desc.Name()), `", err.Error()))`)
				t.P(`        return`)
				t.P(`      }`)
				t.P(`    vs = append(vs, `, ft, fs, `(vvv))`)
				t.P(`    }`)
				t.P(`    reqContent.`, field.GoName, ` = vs`)
			}
		} else {
			if ft == "string" {
				t.P(`    reqContent.`, field.GoName, ` = v[0] `)
			} else {
				if ft == "float" {
					t.P(`    vv, err := strconv.ParseFloat(v[0], `, fs, `)`)
				} else if ft == "bool" {
					t.P(`    vv, err := strconv.ParseBool(v[0])`)
				} else {
					t.P(`    vv, err := strconv.Parse`, exported(ft), `(v[0], 10, `, fs, `)`)
				}
				t.P(`    if err != nil {`)
				t.P(`      s.writeError(ctx, resp, twirp.InvalidArgumentError("`, string(field.Desc.Name()), `", err.Error()))`)
				t.P(`      return`)
				t.P(`    }`)
				t.P(`    reqContent.`, field.GoName, ` = `, ft, fs, `(vv)`)
			}
		}
		t.P(`  }`)
	}
	t.P(`  ctx = twirp.WithRequest(ctx, reqContent)`)
	t.P()

	t.P()
	t.P(`  // Call service method`)
	t.P(`  var respContent *`, t.getType(method.Output))
	t.P(`  func() {`)
	t.P(`    defer func() {`)
	t.P(`      // In case of a panic, serve a 500 error and then panic.`)
	t.P(`      if r := recover(); r != nil {`)
	t.P(`        s.writeError(ctx, resp, `, t.pkgs["twirp"], `.InternalError("Internal service panic"))`)
	t.P(`        panic(r)`)
	t.P(`      }`)
	t.P(`    }()`)
	t.P(`    respContent, err = s.`, methName, `(ctx, reqContent)`)
	t.P(`  }()`)
	t.P()
	t.P(`  if err != nil {`)
	t.P(`    s.writeError(ctx, resp, err)`)
	t.P(`    return`)
	t.P(`  }`)
	t.P(`  if respContent == nil {`)
	t.P(`    s.writeError(ctx, resp, `, t.pkgs["twirp"], `.InternalError("received a nil *`, t.getType(method.Output), ` and nil error while calling `, methName, `. nil responses are not supported"))`)
	t.P(`    return`)
	t.P(`  }`)
	t.P()
	t.P(`  ctx = twirp.WithResponse(ctx, respContent)`)
	t.P()
	t.P(`  ctx = s.hooks.CallResponsePrepared(ctx)`)
	t.P()
	t.P(`  type httpBody interface {`)
	t.P(`    GetContentType() string`)
	t.P(`    GetData() []byte`)
	t.P(`  }`)
	t.P()
	t.P(`  var respBytes []byte`)
	t.P(`  var respStatus = `, t.pkgs["http"], `.StatusOK`)
	t.P(`  if body, ok := interface{}(respContent).(httpBody); ok {`)
	t.P(`    type httpStatus interface{ GetStatus() int32 }`)
	t.P(`    if statusBody, ok := interface{}(respContent).(httpStatus); ok {`)
	t.P(`      if status := statusBody.GetStatus(); status > 0 {`)
	t.P(`        respStatus = int(status)`)
	t.P(`      }`)
	t.P(`    }`)
	t.P(`    if contentType := body.GetContentType(); contentType != "" {`)
	t.P(`      resp.Header().Set("Content-Type", contentType)`)
	t.P(`    }`)
	t.P(`    respBytes = body.GetData()`)
	t.P(`  } else {`)
	t.P(`    var buf `, t.pkgs["bytes"], `.Buffer`)
	t.P(`    marshaler := &`, t.pkgs["jsonpb"], `.Marshaler{OrigName: true, EmitDefaults: true }`)
	t.P(`    if err = marshaler.Marshal(&buf, respContent); err != nil {`)
	t.P(`      err = s.wrapErr(err, "failed to marshal json response")`)
	t.P(`      s.writeError(ctx, resp, `, t.pkgs["twirp"], `.InternalErrorWith(err))`)
	t.P(`      return`)
	t.P(`    }`)
	t.P(`    respBytes = buf.Bytes()`)
	t.P(`    resp.Header().Set("Content-Type", "application/json")`)
	t.P(`  }`)
	t.P()
	t.P(`  ctx = `, t.pkgs["twirp"], `.WithStatusCode(ctx, respStatus)`)
	t.P(`  resp.WriteHeader(respStatus)`)
	t.P()
	t.P(`  if n, err := resp.Write(respBytes); err != nil {`)
	t.P(`    msg := fmt.Sprintf("failed to write response, %d of %d bytes written: %s", n, len(respBytes), err.Error())`)
	t.P(`    twerr := `, t.pkgs["twirp"], `.NewError(`, t.pkgs["twirp"], `.Unknown, msg)`)
	t.P(`    s.hooks.CallError(ctx, twerr)`)
	t.P(`  }`)
	t.P(`  s.hooks.CallResponseSent(ctx)`)
	t.P(`}`)
	t.P()
}

func (t *twirp) generateServerProtobufMethod(service *protogen.Service, method *protogen.Method) {
	servStruct := serviceStruct(service)
	methName := method.GoName
	servName := service.GoName
	t.P(`func (s *`, servStruct, `) serve`, methName, `Protobuf(ctx `, t.pkgs["context"], `.Context, resp `, t.pkgs["http"], `.ResponseWriter, req *`, t.pkgs["http"], `.Request) {`)
	t.P(`  var err error`)
	t.P(`  ctx = `, t.pkgs["twirp"], `.WithMethodName(ctx, "`, methName, `")`)
	t.P(`  ctx, err = s.hooks.CallRequestRouted(ctx)`)
	t.P(`  if err != nil {`)
	t.P(`    s.writeError(ctx, resp, err)`)
	t.P(`    return`)
	t.P(`  }`)
	t.P()
	t.P(`  buf, err := `, t.pkgs["ioutil"], `.ReadAll(req.Body)`)
	t.P(`  if err != nil {`)
	t.P(`    err = s.wrapErr(err, "failed to read request body")`)
	t.P(`    s.writeError(ctx, resp, `, t.pkgs["twirp"], `.InternalErrorWith(err))`)
	t.P(`    return`)
	t.P(`  }`)
	t.P(`  reqContent := new(`, t.getType(method.Input), `)`)
	t.P(`  if err = `, t.pkgs["proto"], `.Unmarshal(buf, reqContent); err != nil {`)
	t.P(`    err = s.wrapErr(err, "failed to parse request proto")`)
	t.P(`    twerr := `, t.pkgs["twirp"], `.NewError(`, t.pkgs["twirp"], `.InvalidArgument, err.Error())`)
	t.P(`    twerr = twerr.WithMeta("cause", `, t.pkgs["fmt"], `.Sprintf("%T", err))`)
	t.P(`    s.writeError(ctx, resp, twerr)`)
	t.P(`    return`)
	t.P(`  }`)
	t.P()
	t.P(`  ctx = twirp.WithRequest(ctx, reqContent)`)
	t.addValidate(method, service)
	t.P(`  // Call service method`)
	t.P(`  var respContent *`, t.getType(method.Output))
	t.P(`  func() {`)
	t.P(`    defer func() {`)
	t.P(`      // In case of a panic, serve a 500 error and then panic.`)
	t.P(`      if r := recover(); r != nil {`)
	t.P(`        s.writeError(ctx, resp, `, t.pkgs["twirp"], `.InternalError("Internal service panic"))`)
	t.P(`        panic(r)`)
	t.P(`      }`)
	t.P(`    }()`)
	t.P(`    respContent, err = s.`, servName, `.`, methName, `(ctx, reqContent)`)
	t.P(`  }()`)
	t.P()
	t.P(`  if err != nil {`)
	t.P(`    s.writeError(ctx, resp, err)`)
	t.P(`    return`)
	t.P(`  }`)
	t.P(`  if respContent == nil {`)
	t.P(`    s.writeError(ctx, resp, `, t.pkgs["twirp"], `.InternalError("received a nil *`, t.getType(method.Output), ` and nil error while calling `, methName, `. nil responses are not supported"))`)
	t.P(`    return`)
	t.P(`  }`)
	t.P()
	t.P(`  ctx = twirp.WithResponse(ctx, respContent)`)
	t.P()
	t.P(`  ctx = s.hooks.CallResponsePrepared(ctx)`)
	t.P()
	t.P(`  type httpBody interface {`)
	t.P(`    GetContentType() string`)
	t.P(`    GetData() []byte`)
	t.P(`  }`)
	t.P()
	t.P(`  var respBytes []byte`)
	t.P(`  var respStatus = `, t.pkgs["http"], `.StatusOK`)
	t.P(`  if body, ok := interface{}(respContent).(httpBody); ok {`)
	t.P(`    type httpStatus interface{ GetStatus() int32 }`)
	t.P(`    if statusBody, ok := interface{}(respContent).(httpStatus); ok {`)
	t.P(`      if status := statusBody.GetStatus(); status > 0 {`)
	t.P(`        respStatus = int(status)`)
	t.P(`      }`)
	t.P(`    }`)
	t.P(`    if contentType := body.GetContentType(); contentType != "" {`)
	t.P(`      resp.Header().Set("Content-Type", contentType)`)
	t.P(`    }`)
	t.P(`    respBytes = body.GetData()`)
	t.P(`  } else {`)
	t.P(`    respBytes, err = `, t.pkgs["proto"], `.Marshal(respContent)`)
	t.P(`    if err != nil {`)
	t.P(`      err = s.wrapErr(err, "failed to marshal proto response")`)
	t.P(`      s.writeError(ctx, resp, `, t.pkgs["twirp"], `.InternalErrorWith(err))`)
	t.P(`      return`)
	t.P(`    }`)
	t.P(`    resp.Header().Set("Content-Type", "application/protobuf")`)
	t.P(`  }`)
	t.P()
	t.P(`  ctx = `, t.pkgs["twirp"], `.WithStatusCode(ctx, respStatus)`)
	t.P(`  resp.WriteHeader(respStatus)`)
	t.P(`  if n, err := resp.Write(respBytes); err != nil {`)
	t.P(`    msg := fmt.Sprintf("failed to write response, %d of %d bytes written: %s", n, len(respBytes), err.Error())`)
	t.P(`    twerr := `, t.pkgs["twirp"], `.NewError(`, t.pkgs["twirp"], `.Unknown, msg)`)
	t.P(`    s.hooks.CallError(ctx, twerr)`)
	t.P(`  }`)
	t.P(`  s.hooks.CallResponseSent(ctx)`)
	t.P(`}`)
	t.P()
}

// serviceMetadataVarName is the variable name used in generated code to refer
// to the compressed bytes of this descriptor. It is not exported, so it is only
// valid inside the generated package.
//
// protoc-gen-go writes its own version of this file, but so does
// protoc-gen-gogo - with a different name! Twirp aims to be compatible with
// both; the simplest way forward is to write the file descriptor again as
// another variable that we control.
func (t *twirp) serviceMetadataVarName(file *protogen.File) string {
	h := sha1.New()
	io.WriteString(h, *file.Proto.Name)
	return fmt.Sprintf("twirpFileDescriptor%dSHA%x", t.filesHandled, h.Sum(nil))
}

func (t *twirp) generateServiceMetadataAccessors(file *protogen.File, service *protogen.Service) {
	servStruct := serviceStruct(service)
	index := 0
	for i, s := range file.Services {
		if s.GoName == service.GoName {
			index = i
			break
		}
	}
	t.P(`func (s *`, servStruct, `) ServiceDescriptor() ([]byte, int) {`)
	t.P(`  return `, t.serviceMetadataVarName(file), `, `, strconv.Itoa(index))
	t.P(`}`)
	t.P()
	t.P(`func (s *`, servStruct, `) ProtocGenTwirpVersion() (string) {`)
	t.P(`  return `, strconv.Quote(Version))
	t.P(`}`)
}

func (t *twirp) generateFileDescriptor(file *protogen.File) {
	// Copied straight of of protoc-gen-go, which trims out comments.
	pb := proto.Clone(file.Proto).(*descriptorpb.FileDescriptorProto)
	pb.SourceCodeInfo = nil

	b, err := proto.Marshal(pb)
	if err != nil {
		log.Fatal(err)
	}

	var buf bytes.Buffer
	w, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	w.Write(b)
	w.Close()
	b = buf.Bytes()

	v := t.serviceMetadataVarName(file)
	t.P()
	t.P("var ", v, " = []byte{")
	t.P("	// ", fmt.Sprintf("%d", len(b)), " bytes of a gzipped FileDescriptorProto")
	for len(b) > 0 {
		n := 16
		if n > len(b) {
			n = len(b)
		}

		s := ""
		for _, c := range b[:n] {
			s += fmt.Sprintf("0x%02x,", c)
		}
		t.P(`	`, s)

		b = b[n:]
	}
	t.P("}")
}

func (t *twirp) printComments(comments protogen.CommentSet) bool {
	text := strings.TrimSuffix(comments.Leading.String(), "\n")
	if len(strings.TrimSpace(text)) == 0 {
		return false
	}
	split := strings.Split(text, "\n")
	for _, line := range split {
		t.P(strings.TrimPrefix(line, " "))
	}
	return len(split) > 0
}

func (t *twirp) formattedOutput(raw []byte) []byte {
	// Reformat generated code.
	fset := token.NewFileSet()
	ast, err := parser.ParseFile(fset, "", raw, parser.ParseComments)
	if err != nil {
		// Print out the bad code with line numbers.
		// This should never happen in practice, but it can while changing generated code,
		// so consider this a debugging aid.
		var src bytes.Buffer
		s := bufio.NewScanner(bytes.NewReader(raw))
		for line := 1; s.Scan(); line++ {
			fmt.Fprintf(&src, "%5d\t%s\n", line, s.Bytes())
		}
		log.Fatal("bad Go source code was generated:", err.Error(), "\n"+src.String())
	}

	out := bytes.NewBuffer(nil)
	err = (&printer.Config{Mode: printer.TabIndent | printer.UseSpaces, Tabwidth: 8}).Fprint(out, fset, ast)
	if err != nil {
		log.Fatal("generated Go source code could not be reformatted:", err.Error())
	}

	return out.Bytes()
}

func unexported(s string) string { return strings.ToLower(s[:1]) + s[1:] }

func exported(s string) string { return strings.ToUpper(s[:1]) + s[1:] }

func serviceStruct(service *protogen.Service) string {
	return unexported(service.GoName) + "Server"
}

func (t *twirp) addValidate(method *protogen.Method, service *protogen.Service) {
	if t.ValidateEnable {
		t.P(`  if  validerr := reqContent.validate(); validerr != nil {`)
		t.P(`    s.writeError(ctx, resp, twirp.InvalidArgumentError("argument", validerr.Error()))`)
		t.P(`    return`)
		t.P(`  }`)
		t.P()
		if t.needLogin(method, service) {
			t.P(`  if ctxkit.GetUserID(ctx) == 0 {`)
			t.P(`    s.writeError(ctx, resp, twirp.NewError(twirp.Unauthenticated, "need login"))`)
			t.P(`    return`)
			t.P(`  }`)
			t.P()
		}
	}
}
