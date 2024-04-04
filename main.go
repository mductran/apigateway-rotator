package main

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/apigateway/types"
)

var (
	DEFAULT_REGIONS = []string{
		"us-east-1", "us-east-2", "us-west-1", "us-west-2",
		"eu-west-1", "eu-west-2", "eu-west-3", "eu-central-1",
		"ca-central-1",
	}

	EXTRA_REGIONS = append(DEFAULT_REGIONS,
		"ap-south-1", "ap-northeast-3", "ap-northeast-2",
		"ap-southeast-1", "ap-southeast-2", "ap-northeast-1",
		"sa-east-1",
	)

	ALL_REGIONS = append(EXTRA_REGIONS,
		"ap-east-1", "af-south-1", "eu-south-1", "me-south-1",
		"eu-north-1",
	)
)

// check if an api already exists in region
func ApiExists(name string, client *apigateway.Client) bool {
	output, err := client.GetRestApis(context.TODO(), &apigateway.GetRestApisInput{})
	if err != nil {
		panic(err)
	}

	for _, api := range output.Items {
		if string(*api.Name) == name {
			return true
		}
	}
	return false
}

// initializes the API Gateway in the specified region.
func Initialize(name, site string, client *apigateway.Client) error {
	// if api resource with name already exists, return
	if ApiExists(name, client) {
		return nil
	}

	// create new rest api
	newApi, err := client.CreateRestApi(context.TODO(), &apigateway.CreateRestApiInput{
		Name: &name,
		EndpointConfiguration: &types.EndpointConfiguration{
			Types: []types.EndpointType{
				types.EndpointTypeRegional,
			},
		},
	})
	if err != nil {
		return err
	}

	// create wildcard proxy path
	path := "{proxy+}"
	newResource, err := client.GetResource(context.TODO(), &apigateway.GetResourceInput{
		RestApiId: newApi.Id,
	})
	if err != nil {
		return err
	}
	client.CreateResource(context.TODO(), &apigateway.CreateResourceInput{
		RestApiId: newApi.Id,
		PathPart:  &path,
		ParentId:  newResource.ParentId,
	})

	// allow all methods to new resource
	allowedHttpMethod := "ANY"
	authorizationType := "NONE"
	methodsRequestParams := make(map[string]bool)
	methodsRequestParams["method.request.path.proxy"] = true
	methodsRequestParams["method.request.header.X-My-X-Forwarded-For"] = true
	_, err = client.PutMethod(context.TODO(), &apigateway.PutMethodInput{
		RestApiId:         newApi.Id,
		ResourceId:        newResource.Id,
		HttpMethod:        &allowedHttpMethod,
		AuthorizationType: &authorizationType,
		RequestParameters: methodsRequestParams,
	})
	if err != nil {
		return err
	}

	// make new resource route traffic to new host
	integrationRequestParams := make(map[string]string)
	integrationRequestParams["integration.request.path.proxy"] = "method.request.path.proxy"
	integrationRequestParams["integration.request.header.X-Forwarded-For"] = "method.request.header.X-Forwarded-For"
	_, err = client.PutIntegration(context.TODO(), &apigateway.PutIntegrationInput{
		RestApiId:             newApi.Id,
		ResourceId:            newResource.Id,
		HttpMethod:            &allowedHttpMethod,
		IntegrationHttpMethod: &allowedHttpMethod,
		Uri:                   &site,
		ConnectionType:        types.ConnectionTypeInternet,
		RequestParameters:     integrationRequestParams,
	})
	if err != nil {
		return err
	}

	return nil
}

func main() {
	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		panic(err)
	}

	client := apigateway.NewFromConfig(cfg)
	fmt.Println(ApiExists("sample_rest", client))
	fmt.Println(ApiExists("mango", client))
}
