package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	treeutils "aspect.build/cli/gazelle/common/treesitter"
	"github.com/emirpasic/gods/sets/treeset"
	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/scala"
)

type ParseResult struct {
	File    string
	Imports []string
  Symbols []string
	Package string
	HasMain bool
}

type Parser interface {
	Parse(filePath, source string) (*ParseResult, []error)
}

type ScalaImports struct {
	imports *treeset.Set
}

type treeSitterParser struct {
	Parser

	parser *sitter.Parser
}

func NewParser() Parser {
	sitter := sitter.NewParser()
	sitter.SetLanguage(scala.GetLanguage())

	p := treeSitterParser{
		parser: sitter,
	}

	return &p
}

var ScalaTreeSitterName = "scala"
var ScalaLang = scala.GetLanguage()

func (p *treeSitterParser) Parse(filePath, source string) (*ParseResult, []error) {
	var result = &ParseResult{
		File:    filePath,
		Imports: make([]string, 0),
    Symbols: make([]string, 0),
	}

	errs := make([]error, 0)

	ctx := context.Background()

	sourceCode := []byte(source)

	tree, err := p.parser.ParseCtx(ctx, nil, sourceCode)
	if err != nil {
		errs = append(errs, err)
	}

	if tree != nil {
		rootNode := tree.RootNode()

		// Extract imports from the root nodes
		for i := 0; i < int(rootNode.NamedChildCount()); i++ {
			nodeI := rootNode.NamedChild(i)

      // fmt.Printf("%s\n", nodeI.Type())

			if nodeI.Type() == "package_clause" {
				if result.Package != "" {
					fmt.Printf("Multiple package declarations found in %s\n", filePath)
					os.Exit(1)
				}

				result.Package = readPackageIdentifier(getLoneChild(nodeI, "package_identifier"), sourceCode, false)

			} else if nodeI.Type() == "import_declaration" {
        // import packages are nested stable_identifiers, with the first two packages in
        // the innermost tuple: (((identifier, identifier), identifier), identifier)
        // e.g. path = ((("com", "twitter"), "finagle"), "http")
        path := nodeI.ChildByFieldName("path")
        importPackage := ""
        for path != nil {
            if importPackage != "" {
              importPackage = "." + importPackage
            }
            importPackage = readStableIdentifier(path, sourceCode, false) + importPackage
            path = getLoneChild(path, "stable_identifier")
        }

        selectors := getLoneChild(nodeI, "import_selectors")
        // TODO(jacob): figure out how to do better checks on what type child nodes are
        if selectors == nil {
          if getLoneChild(nodeI, "import_wildcard") != nil {
            result.Imports = append(result.Imports, importPackage + "._")
          } else {
            result.Imports = append(result.Imports, importPackage)
          }
        } else {
          symbols := readImportSelectors(selectors, sourceCode)
          for _, symbol := range(symbols) {
            result.Imports = append(result.Imports, importPackage + "." + symbol)
          }
        }

      } else {
        childSymbols := recursivelyParseSymbols(nodeI, sourceCode, "")
        result.Symbols = append(result.Symbols, childSymbols...)
      }
		}

		treeErrors := treeutils.QueryErrors(ScalaTreeSitterName, ScalaLang, sourceCode, rootNode)
		if treeErrors != nil {
			errs = append(errs, treeErrors...)
		}
	}

	return result, errs
}

func recursivelyParseSymbols(node *sitter.Node, sourceCode []byte, namespace string) []string {
  symbols := make([]string, 0)

  if hasAccessModifier(node) {
    // NOTE(jacob): For now, just assume any access modifier means this symbol is
    //    not exported. Note this is particularly untrue for class constructors.
    return symbols
  }

  if node.Type() == "function_definition" ||
    node.Type() == "type_definition" ||
    node.Type() == "class_definition" ||
    node.Type() == "trait_definition" ||
    node.Type() == "object_definition" {

    name := node.ChildByFieldName("name")
    symbol := namespace + name.Content(sourceCode)
    symbols = append(symbols, symbol)

    if node.Type() == "object_definition" {
      if body := node.ChildByFieldName("body"); body != nil {
        for i := 0; i < int(body.NamedChildCount()); i++ {
          childSymbols := recursivelyParseSymbols(body.NamedChild(i), sourceCode, symbol + ".")
          symbols = append(symbols, childSymbols...)
        }
      }
    }

  } else if node.Type() == "val_definition" || node.Type() == "var_definition" {
    pattern := node.ChildByFieldName("pattern")
    if pattern.Type() == "case_class_pattern" {
      // NOTE(jacob): We could also be binding symbols via pattern case syntax, e.g.
      //    `val Array(one, two) = Array(1, 2)`. Just ignore this for now.
      return symbols
    }

    symbols = append(symbols, namespace + pattern.Content(sourceCode))

  } else if node.Type() != "comment" {
    fmt.Printf("Unknown symbol type: %s\n", node.Type())
  }

  return symbols
}

func hasAccessModifier(node *sitter.Node) bool {
  if modifiers := getLoneChild(node, "modifiers"); modifiers != nil {
    if access_modifier := getLoneChild(modifiers, "access_modifier"); access_modifier != nil {
      return true
    }
  }

  return false
}

func getLoneChild(node *sitter.Node, name string) *sitter.Node {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		if node.NamedChild(i).Type() == name {
			return node.NamedChild(i)
		}
	}

	return nil
}

func readPackageIdentifier(node *sitter.Node, sourceCode []byte, ignoreLast bool) string {
	if node.Type() != "package_identifier" {
		fmt.Printf("Must be type 'package_identifier': %v - %s", node.Type(), node.Content(sourceCode))
		os.Exit(1)
	}

	var s strings.Builder

	total := int(node.NamedChildCount())
	if ignoreLast {
		total = total - 1
	}

	for c := 0; c < total; c++ {
		nodeC := node.NamedChild(c)

		// TODO: are there any other node types under an "identifier"

		if nodeC.Type() == "identifier" {
			if s.Len() > 0 {
				s.WriteString(".")
			}
			s.WriteString(nodeC.Content(sourceCode))
		} else {
			fmt.Printf("Unexpected node type '%v' within: %s", nodeC.Type(), node.Content(sourceCode))
			os.Exit(1)
		}
	}

	return s.String()
}

func readStableIdentifier(node *sitter.Node, sourceCode []byte, ignoreLast bool) string {
	if node.Type() != "stable_identifier" {
		fmt.Printf("Must be type 'stable_identifier': %v - %s", node.Type(), node.Content(sourceCode))
		os.Exit(1)
	}

	var s strings.Builder

	total := int(node.NamedChildCount())
	if ignoreLast {
		total = total - 1
	}

	for c := 0; c < total; c++ {
		nodeC := node.NamedChild(c)

		// TODO: are there any other node types under a "stable_identifier"

		if nodeC.Type() == "identifier" {
			if s.Len() > 0 {
				s.WriteString(".")
			}
			s.WriteString(nodeC.Content(sourceCode))
		} else if nodeC.Type() != "stable_identifier" {
			fmt.Printf("Unexpected node type '%v' within: %s", nodeC.Type(), node.Content(sourceCode))
			os.Exit(1)
		}
	}

	return s.String()
}

func readImportSelectors(node *sitter.Node, sourceCode []byte) []string {
	if node.Type() != "import_selectors" {
		fmt.Printf("Must be type 'package_identifier': %v - %s", node.Type(), node.Content(sourceCode))
		os.Exit(1)
	}

	total := int(node.NamedChildCount())
	imports := make([]string, total)

	for c := 0; c < total; c++ {
		nodeC := node.NamedChild(c)

		// TODO: are there any other node types under an "identifier"

		if nodeC.Type() == "identifier" {
			imports[c] = nodeC.Content(sourceCode)
		} else if nodeC.Type() == "renamed_identifier" {
      // see also: nodeC.ChildByFieldName("alias")
      imports[c] = nodeC.ChildByFieldName("name").Content(sourceCode)
    } else {
			fmt.Printf("Unexpected node type '%v' within: %s", nodeC.Type(), node.Content(sourceCode))
			os.Exit(1)
		}
	}

	return imports
}

func readIdentifier(node *sitter.Node, sourceCode []byte, ignoreLast bool) string {
	if node.Type() != "identifier" {
		fmt.Printf("Must be type 'identifier': %v - %s", node.Type(), node.Content(sourceCode))
		os.Exit(1)
	}

	var s strings.Builder

	total := int(node.NamedChildCount())
	if ignoreLast {
		total = total - 1
	}

	for c := 0; c < total; c++ {
		nodeC := node.NamedChild(c)

		// TODO: are there any other node types under an "identifier"

		if nodeC.Type() == "simple_identifier" {
			if s.Len() > 0 {
				s.WriteString(".")
			}
			s.WriteString(nodeC.Content(sourceCode))
		} else if nodeC.Type() != "comment" {
			fmt.Printf("Unexpected node type '%v' within: %s", nodeC.Type(), node.Content(sourceCode))
			os.Exit(1)
		}
	}

	return s.String()
}

func main() {
    filePath := os.Args[1]

    fileBytes, err := os.ReadFile(filePath)
    if err != nil {
        panic(err)
    }
    sourceString := string(fileBytes)

    parser := NewParser()
    parseResult, errs := parser.Parse(filePath, sourceString)
    if len(errs) != 0 {
        fmt.Printf("%+v\n", errs)
    }
    fmt.Printf("%+v\n", *parseResult)
}

