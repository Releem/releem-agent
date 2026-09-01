package utils

// NumericKind names which field of Number carries the widened value.
type NumericKind int

const (
	// NotNumeric means the value was not a Go integer or floating-point type.
	// Strings, json.Number, bool, and nil all report NotNumeric so each caller
	// keeps its own parsing and error policy for them.
	NotNumeric NumericKind = iota
	SignedNumber
	UnsignedNumber
	FloatNumber
)

// Number is a Go numeric value widened to its largest form. Kind says which
// field is populated; FloatBits carries the source precision so a float can be
// formatted back without inventing digits it never had.
type Number struct {
	Kind      NumericKind
	Int       int64
	Uint      uint64
	Float     float64
	FloatBits int
}

// AsNumber widens any Go integer or floating-point value, so callers that need
// to coerce a decoded interface{} do not each re-enumerate Go's numeric types.
// It applies no policy of its own: range checks, non-finite handling, and
// formatting stay with the caller.
func AsNumber(value interface{}) Number {
	switch typed := value.(type) {
	case int:
		return Number{Kind: SignedNumber, Int: int64(typed)}
	case int8:
		return Number{Kind: SignedNumber, Int: int64(typed)}
	case int16:
		return Number{Kind: SignedNumber, Int: int64(typed)}
	case int32:
		return Number{Kind: SignedNumber, Int: int64(typed)}
	case int64:
		return Number{Kind: SignedNumber, Int: typed}
	case uint:
		return Number{Kind: UnsignedNumber, Uint: uint64(typed)}
	case uint8:
		return Number{Kind: UnsignedNumber, Uint: uint64(typed)}
	case uint16:
		return Number{Kind: UnsignedNumber, Uint: uint64(typed)}
	case uint32:
		return Number{Kind: UnsignedNumber, Uint: uint64(typed)}
	case uint64:
		return Number{Kind: UnsignedNumber, Uint: typed}
	case float32:
		return Number{Kind: FloatNumber, Float: float64(typed), FloatBits: 32}
	case float64:
		return Number{Kind: FloatNumber, Float: typed, FloatBits: 64}
	default:
		return Number{Kind: NotNumeric}
	}
}
