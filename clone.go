package acpruntime

import "reflect"

// cloneOwned makes an ownership boundary without a JSON round trip: concrete
// numeric types, nil containers and extension values keep their Go types.
// Functions/channels are immutable references; unexported struct fields (for
// example time.Time's location) are copied as values.
func cloneOwned[T any](input T) T {
	v := reflect.ValueOf(input)
	if !v.IsValid() {
		return input
	}
	return cloneValue(v, make(map[cloneVisit]reflect.Value)).Interface().(T)
}

type cloneVisit struct {
	kind   reflect.Kind
	typ    reflect.Type
	ptr    uintptr
	length int
}

func cloneValue(v reflect.Value, seen map[cloneVisit]reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(cloneValue(v.Elem(), seen))
		return out
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		key := cloneVisit{kind: v.Kind(), typ: v.Type()}
		if v.Kind() == reflect.Map {
			key.ptr = uintptr(v.UnsafePointer())
		} else {
			key.ptr = v.Pointer()
		}
		if v.Kind() == reflect.Slice {
			key.length = v.Len()
		}
		if prior, ok := seen[key]; ok {
			return prior
		}
		switch v.Kind() {
		case reflect.Pointer:
			out := reflect.New(v.Type().Elem())
			seen[key] = out
			out.Elem().Set(cloneValue(v.Elem(), seen))
			return out
		case reflect.Map:
			out := reflect.MakeMapWithSize(v.Type(), v.Len())
			seen[key] = out
			iter := v.MapRange()
			for iter.Next() {
				out.SetMapIndex(cloneValue(iter.Key(), seen), cloneValue(iter.Value(), seen))
			}
			return out
		case reflect.Slice:
			out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
			seen[key] = out
			for i := 0; i < v.Len(); i++ {
				out.Index(i).Set(cloneValue(v.Index(i), seen))
			}
			return out
		}
	case reflect.Array:
		out := reflect.New(v.Type()).Elem()
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(cloneValue(v.Index(i), seen))
		}
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				out.Field(i).Set(cloneValue(v.Field(i), seen))
			}
		}
		return out
	}
	return v
}

// cloneStartOptions copies only configuration owned by the runtime. Authorities
// are live host services (often containing mutexes/process handles), never data
// snapshots; their identity must be preserved.
func cloneStartOptions(input StartSessionOptions) StartSessionOptions {
	input.Agent = cloneOwned(input.Agent)
	input.MCPServers = cloneOwned(input.MCPServers)
	input.AdditionalDirectories = cloneOwned(input.AdditionalDirectories)
	input.InitialConfig = cloneOwned(input.InitialConfig)
	input.Meta = cloneOwned(input.Meta)
	input.AgentConfig = cloneOwned(input.AgentConfig)
	return input
}
