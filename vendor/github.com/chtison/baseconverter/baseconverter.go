// Package baseconverter is a set of functions which perform numerical base
// conversion.
package baseconverter

import (
	"container/list"
	"fmt"
	"math/big"
)

type (
	// ErrBaseLengthTooShort is thrown when the length of a base is less than 2.
	//
	// The underlying uint is the length of the base.
	ErrBaseLengthTooShort uint
	// ErrDuplicateCharInBase is thrown when there is (at least) one duplicate character in base.
	//
	// The underlying rune is the found duplicated character.
	ErrDuplicateCharInBase rune
	// ErrCharNotInBase is thrown when there is no correspondence for a character in a base.
	//
	// The underlying rune is the homeless character.
	ErrCharNotInBase rune
)

func (err ErrBaseLengthTooShort) Error() string {
	return fmt.Sprintf("base length too short: %d", uint(err))
}

func (err ErrDuplicateCharInBase) Error() string {
	return fmt.Sprintf("duplicate character in base: '%c'", rune(err))
}

func (err ErrCharNotInBase) Error() string {
	return fmt.Sprintf("number's character not in base: '%c'", rune(err))
}

/*
BaseToDecimal converts a number in any base to a number in base decimal.
All runes in the number string must be present in the inBase string.
An empty number string returns 0 as result and no error.
If BaseToDecimal returns an error, BaseToDecimal returns 0 as result.
BaseToDecimal can return the following errors:
	ErrBaseLengthTooShort(baseLength)     // if len(inBase) < 2
	ErrDuplicateCharInBase(duplicateRune) // if inBase[i] == inBase[j] with i != j
	ErrCharNotInBase(missingRune)         // if number[i] is not in inBase
*/
func BaseToDecimal(number string, inBase string) (result *big.Int, err error) {
	result = big.NewInt(0)
	base := []rune(inBase)
	if err = CheckBase(base); err != nil {
		return
	}
	baseLen := big.NewInt(int64(len(base)))
	for _, c := range number {
		i := indexRune(base, c)
		if i < 0 {
			err = ErrCharNotInBase(c)
			result.SetInt64(0)
			return
		}
		result.Mul(result, baseLen).Add(result, big.NewInt(int64(i)))
	}
	return
}

/*
DecimalToBase converts a number in decimal base to the the specified base.
If a number is negative, DecimalToBase will operate on its absolute value
(without modifying the real number value).
DecimalToBase can return the following errors:
	ErrBaseLengthTooShort(baseLength)     // if len(toBase) < 2
	ErrDuplicateCharInBase(duplicateRune) // if toBase[i] == toBase[j] with i != j
*/
func DecimalToBase(number *big.Int, toBase string) (result string, err error) {
	base := []rune(toBase)
	if err = CheckBase(base); err != nil {
		return
	}
	zero := big.NewInt(0)
	nbr := big.NewInt(0).Abs(number)
	if nbr.Cmp(zero) == 0 {
		return string(base[0]), nil
	}
	baseLen := big.NewInt(int64(len(base)))
	modulo := big.NewInt(0)
	l := list.New()
	for nbr.Cmp(zero) > 0 {
		nbr.QuoRem(nbr, baseLen, modulo)
		l.PushFront(base[modulo.Uint64()])
	}
	a := make([]rune, l.Len())
	for e, i := l.Front(), 0; e != nil; e, i = e.Next(), i+1 {
		a[i] = e.Value.(rune)
	}
	return string(a), nil
}

/*
UInt64ToBase is similar to DecimalToBase function but takes a number as uint64
type instead of a *math/big.Int.
*/
func UInt64ToBase(number uint64, toBase string) (result string, err error) {
	nbr := big.NewInt(0).SetUint64(number)
	return DecimalToBase(nbr, toBase)
}

/*
BaseToBase converts a number in base inBase to base toBase.
BaseToBase returns in e1 error from BaseToDecimal function and in e2 error from
DecimalToBase function.
*/
func BaseToBase(number string, inBase string, toBase string) (result string, e1, e2 error) {
	nbr, e1 := BaseToDecimal(number, inBase)
	if e1 != nil {
		return
	}
	result, e2 = DecimalToBase(nbr, toBase)
	return
}

/*
CheckBase checks if a base has a length less than 2 or any duplicate character.
CheckBase can return the following errors:
	ErrBaseLengthTooShort(baseLength)     // if len(base) < 2
	ErrDuplicateCharInBase(duplicateRune) // if base[i] == base[j] with i != j
*/
func CheckBase(base []rune) error {
	if len(base) < 2 {
		return ErrBaseLengthTooShort(len(base))
	}
	for i := range base {
		for j := range base[i+1:] {
			if base[i] == base[i+1+j] {
				return ErrDuplicateCharInBase(base[i])
			}
		}
	}
	return nil
}

// Same behavior than strings.IndexRune(:,:) but takes a slice of rune instead of a string.
func indexRune(s []rune, c rune) int {
	for i := range s {
		if s[i] == c {
			return i
		}
	}
	return -1
}
