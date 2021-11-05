package controller

// import (
// 	"context"
// 	"reflect"
// 	"testing"

// 	"laisky-blog-graphql/internal/web/general"
// )

// func Test_queryResolver_Lock(t *testing.T) {
// 	type fields struct {
// 		Resolver *Resolver
// 	}
// 	type args struct {
// 		ctx  context.Context
// 		name string
// 	}
// 	tests := []struct {
// 		name    string
// 		fields  fields
// 		args    args
// 		want    *general.Lock
// 		wantErr bool
// 	}{
// 		// TODO: Add test cases.
// 	}
// 	for _, tt := range tests {
// 		t.Run(tt.name, func(t *testing.T) {
// 			q := &queryResolver{
// 				Resolver: tt.fields.Resolver,
// 			}
// 			got, err := q.Lock(tt.args.ctx, tt.args.name)
// 			if (err != nil) != tt.wantErr {
// 				t.Errorf("queryResolver.Lock() error = %v, wantErr %v", err, tt.wantErr)
// 				return
// 			}
// 			if !reflect.DeepEqual(got, tt.want) {
// 				t.Errorf("queryResolver.Lock() = %v, want %v", got, tt.want)
// 			}
// 		})
// 	}
// }
