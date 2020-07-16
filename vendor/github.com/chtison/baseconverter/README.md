# Golang package baseconverter

[![GoDoc](https://godoc.org/github.com/chtison/baseconverter?status.svg)](https://godoc.org/github.com/chtison/baseconverter)

Package baseconverter is a set of functions which perform base conversion.

## Quickstart

A **number** is represented as a \*math/big.Int in decimal base or as a string (interpreted as UTF-8 encoded) in any base.

```go
var number1 *big.Int = big.NewInt(42)   // decimal base (base 10)
var number2 string = "this is a number" // this is a number
var base string = "this anumber"     // this could be the base of number above
```

A **base** is represented as a string (interpreted as UTF-8 encoded), and must own at least two different runes.

```go
var base1 string = "0123456789"       // decimal base
var base2 string = "0123456789ABCDEF" // hexadecimal base
var base3 string = "01"               // base 2
var base4 string = "xy"               // base 2
```

#### For example, you can convert a decimal number to base 16:
```go
package main

import (
	"fmt"

	bc "github.com/chtison/baseconverter"
)

func main() {
	nbrInBase16, _ := bc.UInt64ToBase(51966, "0123456789ABCDEF")
	fmt.Println(nbrInBase16)
}
```
> CAFE

#### Or convert back a number in base "01" (base 2) to base 10:
```go
package main

import (
	"fmt"

	bc "github.com/chtison/baseconverter"
)

func main() {
	nbr, _ := bc.BaseToDecimal("101010", "01")
	fmt.Println(nbr)
}
```
> 42

#### Or convert a number from any base to any other:
```go
package main

import (
	"fmt"

	bc "github.com/chtison/baseconverter"
)

func main() {
	var number string = "🌴🐭🌞🌝🍀💎💎🌝🐱🍀💜🍀🐵🐱🐭🌴🐼🌵🍀🐱💎🐼"
	var inBase string = "🌵🐱🚗🌍🌞🍀💎💰🐼🍋🐵🌴💜🐭🌝"
	var toBase string = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ !"
	converted, _, _ := bc.BaseToBase(number, inBase, toBase)
	fmt.Println(converted)
}
```
> Hello Gophers !
