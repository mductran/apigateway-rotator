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
	newApiResource, err := client.GetResource(context.TODO(), &apigateway.GetResourceInput{
		RestApiId: newApi.Id,
	})
	if err != nil {
		return err
	}

	// creates a resource to handle incoming requests
	incomingHandler, err := client.CreateResource(context.TODO(), &apigateway.CreateResourceInput{
		RestApiId: newApi.Id,
		PathPart:  &path,
		ParentId:  newApiResource.ParentId,
	})
	if err != nil {
		return err
	}

	// configures to allowe all HTTP methods on the created resource
	allowedHttpMethod := "ANY"
	authorizationType := "NONE"
	proxyMethodsRequestParams := make(map[string]bool)
	proxyMethodsRequestParams["method.request.path.proxy"] = true                  // ensures the path portion of the incoming request URL gets forwarded to the target site
	proxyMethodsRequestParams["method.request.header.X-My-X-Forwarded-For"] = true // preserve X-Forwarded-For header by using a temp header X-My-X-Fowarded-For
	_, err = client.PutMethod(context.TODO(), &apigateway.PutMethodInput{
		RestApiId:         newApi.Id,
		ResourceId:        newApiResource.Id,
		HttpMethod:        &allowedHttpMethod,
		AuthorizationType: &authorizationType,
		RequestParameters: proxyMethodsRequestParams,
	})
	if err != nil {
		return err
	}

	// configures the new api gateway resource to integrate with the target site
	proxyIntegrationRequestParams := make(map[string]string)
	proxyIntegrationRequestParams["integration.request.path.proxy"] = "method.request.path.proxy"
	proxyIntegrationRequestParams["integration.request.header.X-Forwarded-For"] = "method.request.header.X-Forwarded-For"
	_, err = client.PutIntegration(context.TODO(), &apigateway.PutIntegrationInput{
		RestApiId:             newApi.Id,
		ResourceId:            newApiResource.Id,
		HttpMethod:            &allowedHttpMethod,
		IntegrationHttpMethod: &allowedHttpMethod,
		Uri:                   &site,
		ConnectionType:        types.ConnectionTypeInternet,
		RequestParameters:     proxyIntegrationRequestParams,
	})
	if err != nil {
		return err
	}

	// handle requests received for the resource created in block 5
	_, err = client.PutMethod(context.TODO(), &apigateway.PutMethodInput{
		RestApiId:         newApi.Id,
		ResourceId:        incomingHandler.Id,
		HttpMethod:        &allowedHttpMethod,
		AuthorizationType: &authorizationType,
		RequestParameters: proxyMethodsRequestParams,
	})
	if err != nil {
		return err
	}

	// configure for paths with trailing forward slash
	destinationUri := site + site + "/proxy"
	_, err = client.PutIntegration(context.TODO(), &apigateway.PutIntegrationInput{
		RestApiId:             newApi.Id,
		ResourceId:            newApiResource.Id,
		Type:                  types.IntegrationTypeHttpProxy,
		HttpMethod:            &allowedHttpMethod,
		IntegrationHttpMethod: &allowedHttpMethod,
		Uri:                   &destinationUri,
		ConnectionType:        types.ConnectionTypeInternet,
		RequestParameters:     proxyIntegrationRequestParams,
	})
	if err != nil {
		return err
	}

	// create deployment resource so the new API is callable
	stageName := "ProxyStage"
	_, err = client.CreateDeployment(context.TODO(), &apigateway.CreateDeploymentInput{
		RestApiId: newApi.Id,
		StageName: &stageName,
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
