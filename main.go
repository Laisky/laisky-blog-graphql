package main

import (
	"github.com/Laisky/laisky-blog-graphql/cmd"
)

//go:generate make gen

func main() {
	cmd.Execute()
}
