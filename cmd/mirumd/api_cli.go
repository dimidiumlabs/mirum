// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// Admin CLI is generated from admin.proto at startup via protoreflect.
// RPC name is camelCase-split into a cobra path: UserCreate -> "user create",
// OrgMemberAdd -> "org member add". Flags come from request fields, dispatch
// goes through reflect on pbconnect.AdminClient.

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"os"
	"reflect"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/timestamppb"

	"dimidiumlabs/mirum/internal/protocol/pb"
	"dimidiumlabs/mirum/internal/protocol/pb/pbconnect"
)

// mkClient is called per-invocation so persistent flags (e.g. --socket) are
// already parsed by the time it runs.
func buildAdminCLI(root *cobra.Command, mkClient func() pbconnect.AdminClient) {
	methods := pb.File_admin_proto.Services().ByName("Admin").Methods()
	for i := 0; i < methods.Len(); i++ {
		md := methods.Get(i)
		path := splitCamel(string(md.Name()))
		parent := ensureGroups(root, path[:len(path)-1])
		parent.AddCommand(buildMethodCmd(md, path[len(path)-1], mkClient))
	}
}

// "OrgMemberAdd" -> ["org","member","add"].
func splitCamel(s string) []string {
	var parts []string
	start := 0
	for i := 1; i < len(s); i++ {
		if unicode.IsUpper(rune(s[i])) {
			parts = append(parts, strings.ToLower(s[start:i]))
			start = i
		}
	}
	return append(parts, strings.ToLower(s[start:]))
}

func ensureGroups(root *cobra.Command, path []string) *cobra.Command {
	parent := root
	for _, name := range path {
		var next *cobra.Command
		for _, c := range parent.Commands() {
			if c.Name() == name {
				next = c
				break
			}
		}
		if next == nil {
			next = &cobra.Command{Use: name, Short: "Manage " + name}
			parent.AddCommand(next)
		}
		parent = next
	}
	return parent
}

// fieldSetter writes one flag value into the request message.
type fieldSetter func(*pflag.FlagSet, protoreflect.Message) error

func buildMethodCmd(md protoreflect.MethodDescriptor, leaf string, mkClient func() pbconnect.AdminClient) *cobra.Command {
	reqDesc := md.Input()
	rpcName := string(md.Name())
	cmd := &cobra.Command{
		Use:   leaf,
		Short: rpcName,
	}
	setters := registerRequestFlags(cmd, reqDesc)
	cmd.Run = func(c *cobra.Command, _ []string) {
		req, err := buildRequest(reqDesc, c.Flags(), setters)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		resp, err := dispatchAdmin(mkClient(), rpcName, req)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		printResponse(resp)
	}
	return cmd
}

func registerRequestFlags(cmd *cobra.Command, desc protoreflect.MessageDescriptor) []fieldSetter {
	var setters []fieldSetter
	fields := desc.Fields()
	for i := 0; i < fields.Len(); i++ {
		if s := registerField(cmd, fields.Get(i)); s != nil {
			setters = append(setters, s)
		}
	}
	return setters
}

func registerField(cmd *cobra.Command, fd protoreflect.FieldDescriptor) fieldSetter {
	flagName := strings.ReplaceAll(string(fd.Name()), "_", "-")
	// Required iff non-optional in the schema; bools default to false, so
	// marking them required makes no sense.
	required := !fd.HasOptionalKeyword() && fd.Kind() != protoreflect.BoolKind
	flags := cmd.Flags()

	markRequired := func() {
		if required {
			_ = cmd.MarkFlagRequired(flagName)
		}
	}

	switch fd.Kind() {
	case protoreflect.StringKind:
		flags.String(flagName, "", string(fd.Name()))
		markRequired()
		return func(fs *pflag.FlagSet, m protoreflect.Message) error {
			v, _ := fs.GetString(flagName)
			if v == "" {
				return nil
			}
			m.Set(fd, protoreflect.ValueOfString(v))
			return nil
		}

	case protoreflect.BoolKind:
		flags.Bool(flagName, false, string(fd.Name()))
		return func(fs *pflag.FlagSet, m protoreflect.Message) error {
			// Preserve "unset" vs "false" for optional bools.
			if fd.HasOptionalKeyword() && !fs.Changed(flagName) {
				return nil
			}
			v, _ := fs.GetBool(flagName)
			m.Set(fd, protoreflect.ValueOfBool(v))
			return nil
		}

	case protoreflect.BytesKind:
		flags.String(flagName, "", string(fd.Name()))
		markRequired()
		return func(fs *pflag.FlagSet, m protoreflect.Message) error {
			v, _ := fs.GetString(flagName)
			if v == "" {
				return nil
			}
			b, err := parseBytesFlag(string(fd.Name()), v)
			if err != nil {
				return fmt.Errorf("--%s: %w", flagName, err)
			}
			m.Set(fd, protoreflect.ValueOfBytes(b))
			return nil
		}

	case protoreflect.EnumKind:
		flags.String(flagName, "", string(fd.Name()))
		markRequired()
		return func(fs *pflag.FlagSet, m protoreflect.Message) error {
			v, _ := fs.GetString(flagName)
			if v == "" {
				return nil
			}
			n, err := parseEnumFlag(fd.Enum(), v)
			if err != nil {
				return fmt.Errorf("--%s: %w", flagName, err)
			}
			m.Set(fd, protoreflect.ValueOfEnum(n))
			return nil
		}

	case protoreflect.MessageKind:
		return registerMessageField(cmd, fd, flagName, required)
	}

	if required {
		panic(fmt.Sprintf("admincli: unhandled required field %s (kind=%s)", fd.FullName(), fd.Kind()))
	}
	return nil
}

// Only UserRef/OrgRef are flattened (to the string arm of their oneof);
// PageRequest is skipped; anything else required panics at startup so
// schema changes can't silently send malformed requests.
func registerMessageField(cmd *cobra.Command, fd protoreflect.FieldDescriptor, flagName string, required bool) fieldSetter {
	flags := cmd.Flags()
	markRequired := func() {
		if required {
			_ = cmd.MarkFlagRequired(flagName)
		}
	}

	switch fd.Message().FullName() {
	case "mirum.UserRef":
		flags.String(flagName, "", "user email")
		markRequired()
		return func(fs *pflag.FlagSet, m protoreflect.Message) error {
			v, _ := fs.GetString(flagName)
			if v == "" {
				return nil
			}
			ref := &pb.UserRef{Ref: &pb.UserRef_Email{Email: v}}
			m.Set(fd, protoreflect.ValueOfMessage(ref.ProtoReflect()))
			return nil
		}

	case "mirum.OrgRef":
		flags.String(flagName, "", "org slug")
		markRequired()
		return func(fs *pflag.FlagSet, m protoreflect.Message) error {
			v, _ := fs.GetString(flagName)
			if v == "" {
				return nil
			}
			ref := &pb.OrgRef{Ref: &pb.OrgRef_Slug{Slug: v}}
			m.Set(fd, protoreflect.ValueOfMessage(ref.ProtoReflect()))
			return nil
		}

	case "mirum.PageRequest":
		return nil
	}

	if required {
		panic(fmt.Sprintf("admincli: unhandled required message field %s (type=%s)", fd.FullName(), fd.Message().FullName()))
	}
	return nil
}

// Admin schema uses bytes only for UUIDs (id / *_id) and ed25519 PKIX keys.
func parseBytesFlag(fieldName, v string) ([]byte, error) {
	switch {
	case fieldName == "id" || strings.HasSuffix(fieldName, "_id"):
		id, err := ParseAnyID(v)
		if err != nil {
			return nil, fmt.Errorf("invalid id: %w", err)
		}
		return id[:], nil
	case strings.Contains(fieldName, "key"):
		der, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return nil, fmt.Errorf("invalid base64: %w", err)
		}
		pub, err := x509.ParsePKIXPublicKey(der)
		if err != nil {
			return nil, fmt.Errorf("invalid public key: %w", err)
		}
		ed, ok := pub.(ed25519.PublicKey)
		if !ok {
			return nil, fmt.Errorf("not an ed25519 key")
		}
		return ed, nil
	}
	return nil, fmt.Errorf("unsupported bytes field %q", fieldName)
}

// Accepts both short ("admin") and full ("ROLE_ADMIN") forms.
func parseEnumFlag(ed protoreflect.EnumDescriptor, v string) (protoreflect.EnumNumber, error) {
	want := strings.ToUpper(v)
	values := ed.Values()
	if ev := values.ByName(protoreflect.Name(want)); ev != nil {
		return ev.Number(), nil
	}
	prefix := strings.ToUpper(string(ed.Name())) + "_"
	if ev := values.ByName(protoreflect.Name(prefix + want)); ev != nil {
		return ev.Number(), nil
	}
	return 0, fmt.Errorf("unknown %s value %q", ed.Name(), v)
}

func buildRequest(desc protoreflect.MessageDescriptor, flags *pflag.FlagSet, setters []fieldSetter) (proto.Message, error) {
	mt, err := protoregistry.GlobalTypes.FindMessageByName(desc.FullName())
	if err != nil {
		return nil, fmt.Errorf("find message %s: %w", desc.FullName(), err)
	}
	m := mt.New()
	for _, set := range setters {
		if err := set(flags, m); err != nil {
			return nil, err
		}
	}
	return m.Interface(), nil
}

// reflect.New on *connect.Request[T] is equivalent to connect.NewRequest(req):
// Msg is the only public field, the rest are initialised lazily at send time.
func dispatchAdmin(client pbconnect.AdminClient, name string, req proto.Message) (proto.Message, error) {
	cv := reflect.ValueOf(client)
	method := cv.MethodByName(name)
	if !method.IsValid() {
		return nil, fmt.Errorf("unknown admin method %q", name)
	}
	// method signature:
	//   func(context.Context, *connect.Request[T]) (*connect.Response[U], error)
	reqPtrType := method.Type().In(1) // *connect.Request[T]
	reqWrap := reflect.New(reqPtrType.Elem())
	reqWrap.Elem().FieldByName("Msg").Set(reflect.ValueOf(req))

	out := method.Call([]reflect.Value{
		reflect.ValueOf(context.Background()),
		reqWrap,
	})
	if errV := out[1]; !errV.IsNil() {
		return nil, errV.Interface().(error)
	}
	return out[0].Elem().FieldByName("Msg").Interface().(proto.Message), nil
}

// Shape-driven printer: empty → "ok", single bytes id → UUID, single message
// → TSV of scalars, repeated → one TSV line per element, anything else → TSV
// of top-level scalars. PageResponse metadata is ignored.
func printResponse(resp proto.Message) {
	m := resp.ProtoReflect()
	fields := m.Descriptor().Fields()

	var meaningful []protoreflect.FieldDescriptor
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		if f.Kind() == protoreflect.MessageKind && f.Message().FullName() == "mirum.PageResponse" {
			continue
		}
		meaningful = append(meaningful, f)
	}

	if len(meaningful) == 0 {
		fmt.Println("ok")
		return
	}
	if len(meaningful) == 1 {
		f := meaningful[0]
		v := m.Get(f)
		switch {
		case f.IsList():
			list := v.List()
			for j := 0; j < list.Len(); j++ {
				item := list.Get(j)
				if f.Kind() == protoreflect.MessageKind {
					fmt.Println(formatMessageTSV(item.Message()))
				} else {
					fmt.Println(formatScalar(f, item))
				}
			}
		case f.Kind() == protoreflect.MessageKind:
			fmt.Println(formatMessageTSV(v.Message()))
		default:
			fmt.Println(formatScalar(f, v))
		}
		return
	}
	fmt.Println(formatMessageTSV(m))
}

func formatMessageTSV(m protoreflect.Message) string {
	var parts []string
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		if f.IsList() || f.IsMap() {
			continue
		}
		if f.HasOptionalKeyword() && !m.Has(f) {
			continue
		}
		if f.Kind() == protoreflect.MessageKind {
			if f.Message().FullName() == "google.protobuf.Timestamp" {
				ts := m.Get(f).Message().Interface().(*timestamppb.Timestamp)
				parts = append(parts, ts.AsTime().Format("2006-01-02"))
				continue
			}
			nested := m.Get(f).Message()
			if nested.IsValid() {
				parts = append(parts, formatMessageTSV(nested))
			}
			continue
		}
		parts = append(parts, formatScalar(f, m.Get(f)))
	}
	return strings.Join(parts, "\t")
}

func formatScalar(fd protoreflect.FieldDescriptor, v protoreflect.Value) string {
	switch fd.Kind() {
	case protoreflect.StringKind:
		return v.String()
	case protoreflect.BoolKind:
		if v.Bool() {
			return "true"
		}
		return "false"
	case protoreflect.BytesKind:
		return formatBytes(v.Bytes())
	case protoreflect.EnumKind:
		ev := fd.Enum().Values().ByNumber(v.Enum())
		if ev == nil {
			return fmt.Sprintf("%d", v.Enum())
		}
		name := string(ev.Name())
		if idx := strings.IndexByte(name, '_'); idx >= 0 {
			name = name[idx+1:]
		}
		return strings.ToLower(name)
	}
	return v.String()
}

func formatBytes(b []byte) string {
	if len(b) == 16 {
		return FormatAnyID(b)
	}
	return base64.StdEncoding.EncodeToString(b)
}
