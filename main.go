package main

import "laisky-blog-graphql/cmd"

//go:generate make gen

func main() {
	cmd.Execute()
}
