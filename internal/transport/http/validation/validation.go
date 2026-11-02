package validation

import "github.com/go-playground/validator/v10"

var engine = validator.New(validator.WithRequiredStructEnabled())

func Struct(value any) error { return engine.Struct(value) }
