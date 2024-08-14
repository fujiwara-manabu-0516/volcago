package generator

import (
	"log"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/fatih/structtag"
	"github.com/go-generalize/go-easyparser/types"
	"github.com/go-generalize/volcago/pkg/fsutil"
	"github.com/go-generalize/volcago/pkg/gocodegen"
	"github.com/go-generalize/volcago/pkg/sliceutil"
	"github.com/go-utils/gopackages"
	"github.com/iancoleman/strcase"
	"golang.org/x/xerrors"
)

const (
	goFileExtension = ".go"
)

type structGenerator struct {
	param templateParameter

	typ        *types.Object
	baseDir    string
	structName string
	opt        GenerateOption
	dupMap     map[string]int
}

func newStructGenerator(typ *types.Object, structName, appVersion string, opt GenerateOption) (*structGenerator, error) {
	g := &structGenerator{
		typ:        typ,
		structName: structName,
		opt:        opt,
		dupMap:     make(map[string]int),
	}

	isSameDir, err := fsutil.IsSamePath(g.baseDir, g.opt.OutputDir)

	if err != nil {
		return nil, xerrors.Errorf("failed to call IsSamePath: %w", err)
	}

	var hasMetaFields bool
	if !opt.DisableMetaFieldsDetection {
		hasMetaFields, err = g.hasMetaFields()

		if err != nil {
			return nil, xerrors.Errorf("meta fields are invalid: %w", err)
		}
	}

	name := g.typ.Position.Filename

	g.param.FileName = strings.TrimSuffix(filepath.Base(name), goFileExtension)
	g.param.GeneratedFileName = g.param.FileName + "_gen"
	g.param.MetaFieldsEnabled = hasMetaFields
	g.param.IsSubCollection = g.opt.Subcollection

	g.param.AppVersion = appVersion
	g.param.RepositoryInterfaceName = structName + "Repository"
	g.param.RepositoryStructName = strcase.ToLowerCamel(g.param.RepositoryInterfaceName)

	g.param.StructName = g.structName
	g.param.StructNameRef = g.structName
	g.param.PackageName = func() string {
		pn := g.opt.PackageName
		if pn == "" {
			return g.typ.PkgName
		}
		return pn
	}()
	g.param.CollectionName = func() string {
		cn := g.opt.CollectionName
		if cn == "" {
			return g.structName
		}
		return cn
	}()

	g.param.MockGenPath = g.opt.MockGenPath
	g.param.MockOutputPath = func() string {
		mop := g.opt.MockOutputPath

		mop = strings.ReplaceAll(mop, "{{ .GeneratedFileName }}", g.param.GeneratedFileName)
		if !strings.HasSuffix(mop, goFileExtension) {
			mop += goFileExtension
		}
		return mop
	}()

	if !isSameDir {
		mod, err := gopackages.NewModule(g.baseDir)

		if err != nil {
			return nil, xerrors.Errorf("failed to initialize gopackages.Module: %w", err)
		}

		importPath, err := mod.GetImportPath(g.baseDir)

		if err != nil {
			return nil, xerrors.Errorf("failed to get import path for current directory: %w", err)
		}

		// Convert backslash into slash for Windows
		importPath = filepath.ToSlash(importPath)

		g.param.StructNameRef = "model." + g.structName
		g.param.ModelImportPath = importPath
	}

	return g, nil
}

func isIgnoredField(tags *structtag.Tags) bool {
	fsTag, err := tags.Get("firestore")
	if err != nil {
		return false
	}

	if _, err = tags.Get("firestore_key"); err == nil {
		return false
	}

	return strings.Split(fsTag.Value(), ",")[0] == "-"
}

func (g *structGenerator) hasMetaFields() (bool, error) {
	const (
		stringType = "string"
		timeType   = "time.Time"
		intType    = "int"
	)

	expectedFields := map[string]struct {
		Type string
	}{
		"CreatedAt": {
			Type: timeType,
		},
		"CreatedBy": {
			Type: stringType,
		},
		"UpdatedAt": {
			Type: timeType,
		},
		"UpdatedBy": {
			Type: stringType,
		},
		"DeletedAt": {
			Type: "*" + timeType,
		},
		"DeletedBy": {
			Type: stringType,
		},
		"Version": {
			Type: intType,
		},
	}

	deleted := false
	for _, v := range g.typ.Entries {
		matched, ok := expectedFields[v.RawName]

		if !ok {
			continue
		}

		expectedType := getGoTypeFromEPTypes(v.Type)

		if expectedType != matched.Type {
			log.Printf("%s in meta fields should be %s, but got %s", v.RawName, expectedType, matched.Type)

			continue
		}

		delete(expectedFields, v.RawName)
		deleted = true
	}

	if len(expectedFields) == 0 {
		return true, nil
	}

	if deleted {
		return false, xerrors.Errorf("meta fields are incomplete")
	}

	return false, nil
}

func (g *structGenerator) parseIndexesField(tags *structtag.Tags) error {
	g.param.EnableIndexes = true
	fieldInfo := &FieldInfo{
		FsTag:     "Indexes",
		Field:     "Indexes",
		FieldType: typeBoolMap,
	}

	tag, err := validateFirestoreTag(tags)
	if err != nil {
		return xerrors.Errorf("firestore tag(%s) is invalid: %w", tag, err)
	} else if tag != "" {
		fieldInfo.FsTag = tag
	}

	g.param.FieldInfoForIndexes = fieldInfo

	return nil
}

func (g *structGenerator) parseType() error {
	if err := g.parseTypeImpl("", "", g.typ, []string{}); err != nil {
		return xerrors.Errorf("failed to parse struct: %w", err)
	}

	return nil
}

func (g *structGenerator) parseTypeImpl(rawKey, firestoreKey string, obj *types.Object, nullableFields []string) error {
	entries := make([]types.ObjectEntry, 0, len(obj.Entries))
	for _, e := range obj.Entries {
		entries = append(entries, e)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].FieldIndex < entries[j].FieldIndex
	})

	for _, e := range entries {
		typeName := getGoTypeFromEPTypes(e.Type)
		pos := e.Position.String()

		fieldRawKey := strings.Join(sliceutil.RemoveEmpty([]string{rawKey, e.RawName}), ".")
		fieldFirestoreKey := firestoreKey

		tags, err := structtag.Parse(e.RawTag)
		if err != nil {
			log.Printf(
				"%s: tag for %s in struct %s in %s",
				pos, e.RawTag, g.structName, g.param.GeneratedFileName+goFileExtension,
			)
			continue
		}

		fsTag, fsTagErr := tags.Get("firestore")
		if fsTagErr != nil {
			fieldFirestoreKey = strings.Join(sliceutil.RemoveEmpty([]string{fieldFirestoreKey, e.RawName}), ".")
		} else {
			fieldFirestoreKey = strings.Join(sliceutil.RemoveEmpty([]string{fieldFirestoreKey, fsTag.Name}), ".")
		}

		if isNestedStruct(e.Type) {
			isNullable := false
			var o *types.Object
			switch entry := e.Type.(type) {
			case *types.Object:
				o = entry
			case *types.Nullable:
				o = entry.Inner.(*types.Object)
				isNullable = true
			default:
				panic("unreachable")
			}

			if isNullable {
				g.param.UseLo = true
				nullableFields = append(nullableFields, fieldRawKey)
			}

			if err = g.parseTypeImpl(fieldRawKey, fieldFirestoreKey, o, nullableFields); err != nil {
				return xerrors.Errorf("failed to parse %s: %w", e.RawName, err)
			}
			continue
		}

		if strings.HasPrefix(typeName, "[]") {
			g.param.SliceExist = true
		}

		if isIgnoredField(tags) {
			continue
		}

		if rawKey == "" && e.RawName == "Indexes" && typeName == typeBoolMap {
			if err = g.parseIndexesField(tags); err != nil {
				return xerrors.Errorf("failed to parse indexes field: %w", err)
			}

			continue
		}

		if e.RawTag == "" {
			fieldInfo := &FieldInfo{
				FsTag:          fieldFirestoreKey,
				Field:          fieldRawKey,
				FieldType:      typeName,
				Indexes:        make([]*IndexesInfo, 0),
				NullableFields: nullableFields,
			}
			if _, err = g.appendIndexer(nil, firestoreKey, fieldInfo); err != nil {
				return xerrors.Errorf("%s: %w", pos, err)
			}
			g.param.FieldInfos = append(g.param.FieldInfos, fieldInfo)
			continue
		}

		tag, err := tags.Get("firestore_key")
		if err != nil {
			fieldInfo := &FieldInfo{
				FsTag:          fieldFirestoreKey,
				Field:          fieldRawKey,
				FieldType:      typeName,
				Indexes:        make([]*IndexesInfo, 0),
				NullableFields: nullableFields,
			}
			if _, err = tags.Get("unique"); err == nil {
				if typeName != typeString {
					return xerrors.Errorf("%s: The only field type that uses the `unique` tag is a string", pos)
				}
				fieldInfo.IsUnique = true
				if g.param.UniqueInfos == nil {
					g.param.UniqueInfos = make([]*UniqueInfo, 0)
				}
				ui := &UniqueInfo{
					Field: fieldInfo.Field,
					FsTag: fieldInfo.FsTag,
				}
				g.param.UniqueInfos = append(g.param.UniqueInfos, ui)
			}
			if fieldInfo, err = g.appendIndexer(tags, firestoreKey, fieldInfo); err != nil {
				return xerrors.Errorf("%s: %w", pos, err)
			}
			g.param.FieldInfos = append(g.param.FieldInfos, fieldInfo)
			continue
		}

		switch tag.Value() {
		case "":
			// ok
		case "auto":
			g.param.AutomaticGeneration = true
		default:
			return xerrors.Errorf(`%s: The contents of the firestore_key tag should be "" or "auto"`, pos)
		}

		// firestore タグが存在しないか-になっていない
		if fsTagErr != nil || fsTag.Name != "-" {
			return xerrors.New("key field for firestore should have firestore:\"-\" tag")
		}

		g.param.KeyFieldName = e.RawName
		g.param.KeyFieldType = typeName

		if g.param.KeyFieldType != typeString {
			return xerrors.New("supported key types are string")
		}

		g.param.KeyValueName = strcase.ToLowerCamel(e.RawName)

		// NOTE: DocumentID検索用
		fieldInfo := &FieldInfo{
			Field:          strings.Join(sliceutil.RemoveEmpty([]string{rawKey, e.RawName}), "."),
			FieldType:      typeName,
			IsDocumentID:   true,
			NullableFields: nullableFields,
		}
		g.param.FieldInfos = append(g.param.FieldInfos, fieldInfo)
	}

	return nil
}

func (g *structGenerator) generate() error {
	templates := template.Must(
		template.New("").
			Funcs(g.getFuncMap()).
			ParseFS(templatesFS, "templates/*.tmpl"),
	)

	gcgen := gocodegen.NewGoCodeGenerator(templates)

	targets := []struct {
		tmplName      string
		generatedName string
	}{
		{"gen.go.tmpl", g.param.GeneratedFileName + goFileExtension},
		{"label.go.tmpl", g.param.FileName + "_label_gen.go"},
		{"constant.go.tmpl", "constant_gen.go"},
		{"errors.go.tmpl", "errors_gen.go"},
		{"misc.go.tmpl", "misc_gen.go"},
		{"query_builder.go.tmpl", "query_builder_gen.go"},
		{"query_chainer.go.tmpl", "query_chain_gen.go"},
		{"unique.go.tmpl", "unique_gen.go"},
	}

	for _, t := range targets {
		if err := gcgen.GenerateTo(t.tmplName, g.param, filepath.Join(g.opt.OutputDir, t.generatedName)); err != nil {
			return xerrors.Errorf("failed to generate %s: %w", t.generatedName, err)
		}
	}

	return nil
}
