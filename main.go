package main

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker/decls"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/interpreter/functions"
	exprpb "google.golang.org/genproto/googleapis/api/expr/v1alpha1"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"log"
	"main/structs"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Poc struct {
	Name       string            `yaml:"name"`
	Transport  string            `yaml:"transport"`
	Set        map[string]string `yaml:"set"`
	Rules      map[string]Rule   `yaml:"rules"`
	Expression string            `yaml:"expression"`
	Detail     Detail            `yaml:"detail"`
}

type Rule struct {
	Request    RuleRequest `yaml:"request"`
	Expression string      `yaml:"expression"`
}

type RuleRequest struct {
	Cache      bool   `yaml:"cache"`
	Method     string `yaml:"method"`
	Path       string `yaml:"path"`
	Body       string `yaml:"body"`
	Expression string `yaml:"expression"`
}

type Detail struct {
	Links []string `yaml:"links"`
}

var poc = Poc{}

func main() {
	pocFile, _ := ioutil.ReadFile("poc.yml")
	err := yaml.Unmarshal(pocFile, &poc)
	if err != nil {
		log.Fatalln(err.Error())
	}

	variableMap := make(map[string]interface{})

	//解析set
	for key, setExpression := range poc.Set {
		value, err := execSetExpression(setExpression)
		if err == nil {
			variableMap[key] = value
		} else {
			log.Println(fmt.Sprintf("set expression %s error", setExpression))
			continue
		}
	}

	println(execPocExpression("http://127.0.0.1:8080", variableMap, poc.Expression, poc.Rules))
}

// 渲染函数 渲染变量到request中
func render(v string, setMap map[string]interface{}) string {
	for k1, v1 := range setMap {
		_, isMap := v1.(map[string]string)
		if isMap {
			continue
		}
		v1Value := fmt.Sprintf("%v", v1)
		t := "{{" + k1 + "}}"
		if !strings.Contains(v, t) {
			continue
		}
		v = strings.ReplaceAll(v, t, v1Value)
	}
	return v
}

var RequestsInvoke = func(target string, setMap map[string]interface{}, rule Rule) bool {
	var req *http.Request
	var err error
	if rule.Request.Body == "" {
		req, err = http.NewRequest(rule.Request.Method, target+render(rule.Request.Path, setMap), nil)
	} else {
		req, err = http.NewRequest(rule.Request.Method, target+render(rule.Request.Path, setMap), bytes.NewBufferString(render(rule.Request.Body, setMap)))
	}
	if err != nil {
		log.Println(fmt.Sprintf("http request error: %s", err.Error()))
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		println(err.Error())
		return false
	}
	response := &structs.Response{}
	response.Body, _ = ioutil.ReadAll(resp.Body)
	return execRuleExpression(rule.Expression, map[string]interface{}{"response": response})
}

func execSetExpression(Expression string) (interface{}, error) {
	//定义set 内部函数接口
	setFuncsInterface := cel.Declarations(
		decls.NewFunction("randomInt",
			decls.NewOverload("randomInt_int_int",
				[]*exprpb.Type{decls.Int, decls.Int},
				decls.String)),
		decls.NewFunction("randomLowercase",
			decls.NewOverload("randomLowercase_string",
				[]*exprpb.Type{decls.Int},
				decls.String)),
	)

	//实现set 内部函数接口
	setFuncsImpl := cel.Functions(
		&functions.Overload{
			Operator: "randomInt_int_int",
			Binary: func(lhs ref.Val, rhs ref.Val) ref.Val {
				randSource := rand.New(rand.NewSource(time.Now().UnixNano()))
				min := int(lhs.Value().(int64))
				max := int(rhs.Value().(int64))
				return types.String(strconv.Itoa(min + randSource.Intn(max-min)))
			}},
		&functions.Overload{
			Operator: "randomLowercase_string",
			Unary: func(lhs ref.Val) ref.Val {
				n := lhs.Value().(int64)
				letterBytes := "abcdefghijklmnopqrstuvwxyz"
				randSource := rand.New(rand.NewSource(time.Now().UnixNano()))
				const (
					letterIdxBits = 6                    // 6 bits to represent a letter index
					letterIdxMask = 1<<letterIdxBits - 1 // All 1-bits, as many as letterIdxBits
					letterIdxMax  = 63 / letterIdxBits   // # of letter indices fitting in 63 bits
				)
				randBytes := make([]byte, n)
				for i, cache, remain := n-1, randSource.Int63(), letterIdxMax; i >= 0; {
					if remain == 0 {
						cache, remain = randSource.Int63(), letterIdxMax
					}
					if idx := int(cache & letterIdxMask); idx < len(letterBytes) {
						randBytes[i] = letterBytes[idx]
						i--
					}
					cache >>= letterIdxBits
					remain--
				}
				return types.String(randBytes)
			}},
	)

	//创建set 执行环境
	env, err := cel.NewEnv(setFuncsInterface)
	if err != nil {
		log.Fatalf("environment creation error: %v\n", err)
	}
	ast, iss := env.Compile(Expression)
	if iss.Err() != nil {
		log.Fatalln(iss.Err())
		return nil, iss.Err()
	}
	prg, err := env.Program(ast, setFuncsImpl)
	if err != nil {
		return nil, errors.New(fmt.Sprintf("Program creation error: %v\n", err))
	}
	out, _, err := prg.Eval(map[string]interface{}{})
	if err != nil {
		log.Fatalf("Evaluation error: %v\n", err)
		return nil, errors.New(fmt.Sprintf("Evaluation error: %v\n", err))
	}
	return out, nil
}

func execRuleExpression(Expression string, variableMap map[string]interface{}) bool {
	env, _ := cel.NewEnv(
		cel.Container("structs"),
		cel.Types(&structs.Response{}),
		cel.Declarations(
			decls.NewVar("response", decls.NewObjectType("structs.Response")),
			decls.NewFunction("bcontains",
				decls.NewInstanceOverload("bytes_bcontains_bytes",
					[]*exprpb.Type{decls.Bytes, decls.Bytes},
					decls.Bool)),
		),
	)
	funcImpl := []cel.ProgramOption{
		cel.Functions(
			&functions.Overload{
				Operator: "bytes_bcontains_bytes",
				Binary: func(lhs ref.Val, rhs ref.Val) ref.Val {
					v1, ok := lhs.(types.Bytes)
					if !ok {
						return types.ValOrErr(lhs, "unexpected type '%v' passed to bcontains", lhs.Type())
					}
					v2, ok := rhs.(types.Bytes)
					if !ok {
						return types.ValOrErr(rhs, "unexpected type '%v' passed to bcontains", rhs.Type())
					}
					return types.Bool(bytes.Contains(v1, v2))
				},
			},
		)}
	ast, iss := env.Compile(Expression)
	if iss.Err() != nil {
		log.Fatalln(iss.Err())
	}
	prg, err := env.Program(ast, funcImpl...)
	if err != nil {
		log.Fatalf("Program creation error: %v\n", err)
	}
	out, _, err := prg.Eval(variableMap)
	if err != nil {
		log.Fatalf("Evaluation error: %v\n", err)
	}
	return out.Value().(bool)
}

func execPocExpression(target string, setMap map[string]interface{}, Expression string, rules map[string]Rule) bool {
	var funcsInterface []*exprpb.Decl
	var funcsImpl []*functions.Overload
	for key, rule := range rules {
		funcName := key
		funcRule := rule
		funcsInterface = append(funcsInterface, decls.NewFunction(key, decls.NewOverload(key, []*exprpb.Type{}, decls.Bool)))
		funcsImpl = append(funcsImpl,
			&functions.Overload{
				Operator: funcName,
				Function: func(values ...ref.Val) ref.Val {
					return types.Bool(RequestsInvoke(target, setMap, funcRule))
				},
			})
	}
	env, err := cel.NewEnv(cel.Declarations(funcsInterface...))
	if err != nil {
		log.Fatalf("environment creation error: %v\n", err)
	}
	ast, iss := env.Compile(Expression)
	if iss.Err() != nil {
		log.Fatalln(iss.Err())
	}
	prg, err := env.Program(ast, cel.Functions(funcsImpl...))
	if err != nil {
		log.Fatalln(fmt.Sprintf("Program creation error: %v\n", err))
	}
	out, _, err := prg.Eval(map[string]interface{}{})
	return out.Value().(bool)
}
