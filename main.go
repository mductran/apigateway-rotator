package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/apigateway/types"
)

var (
	DEFAULT_REGIONS = []string{
		"us-east-1", "us-east-2",
	}

	EXTRA_REGIONS = append(DEFAULT_REGIONS,
		"us-west-1", "us-west-2",
		"eu-west-1", "eu-west-2", "eu-west-3", "eu-central-1",
		"ca-central-1", "ap-south-1", "ap-northeast-3", "ap-northeast-2",
		"ap-southeast-1", "ap-southeast-2", "ap-northeast-1",
		"sa-east-1",
	)

	ALL_REGIONS = append(EXTRA_REGIONS,
		"ap-east-1", "af-south-1", "eu-south-1", "me-south-1",
		"eu-north-1",
	)
)

type apiGateway struct {
	Site      string
	Name      string
	Endpoints []string
	Regions   []string
}

type ApiGateway interface {
	Initialize()
	Start()
	Reroute()
}

func randomIpv4() net.IP {
	buf := make([]byte, 4)
	ip := rand.Uint32()
	binary.LittleEndian.PutUint32(buf, ip)
	return net.IP(buf)
}

func NewApiGateway(site, name string) (*apiGateway, error) {
	if site[len(site)-1] == '/' {
		site = strings.TrimRight(site, "/")
	}

	return &apiGateway{
		Site:      site,
		Name:      name,
		Endpoints: []string{},
		Regions:   DEFAULT_REGIONS,
	}, nil
}

// check if an api already exists in region
func ApiExistsInRegion(client *apigateway.Client, name string, region string) bool {
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
func (ag *apiGateway) Initialize(region string, ctx context.Context) error {

	fmt.Println("initializing")

	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		panic(err)
	}
	cfg.Region = region
	client := apigateway.NewFromConfig(cfg)

	if ApiExistsInRegion(client, ag.Name, region) {
		return fmt.Errorf("an API already exists with name: %s in region %s", ag.Name, region)
	}

	// create new REST API
	newApi, err := client.CreateRestApi(ctx, &apigateway.CreateRestApiInput{
		Name: &ag.Name,
		EndpointConfiguration: &types.EndpointConfiguration{
			Types: []types.EndpointType{
				types.EndpointTypeRegional,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("cannot create new API: %w", err)
	}

	allowedHttpMethod := "ANY"
	authorizationType := "NONE"
	params := make(map[string]bool)
	params["method.request.path.proxy"] = true                  // ensures the path portion of the incoming request URL gets forwarded to the target site
	params["method.request.header.X-Forwarded-For-Temp"] = true // preserve X-Forwarded-For header by using a temp header X-My-X-Fowarded-For

	// allow all methods to new resource
	_, err = client.PutMethod(ctx, &apigateway.PutMethodInput{
		RestApiId:         newApi.Id,
		ResourceId:        newApi.RootResourceId,
		HttpMethod:        &allowedHttpMethod,
		AuthorizationType: &authorizationType,
		RequestParameters: params,
	})
	if err != nil {
		return fmt.Errorf("cannot create method: %w", err)
	}

	// make new resource route traffic to new host
	integrationParams := make(map[string]string)
	integrationParams["integration.request.path.proxy"] = "method.request.path.proxy"
	integrationParams["integration.request.header.X-Forwarded-For"] = "method.request.header.X-Forwarded-For-Temp"
	_, err = client.PutIntegration(ctx, &apigateway.PutIntegrationInput{
		RestApiId:             newApi.Id,
		ResourceId:            newApi.RootResourceId,
		Type:                  types.IntegrationTypeHttpProxy,
		HttpMethod:            &allowedHttpMethod,
		IntegrationHttpMethod: &allowedHttpMethod,
		Uri:                   &ag.Site,
		ConnectionType:        types.ConnectionTypeInternet,
		RequestParameters:     integrationParams,
	})
	if err != nil {
		return fmt.Errorf("cannot create integration: %w", err)
	}

	wildcardPath := "{proxy+}"
	wildcardHandler, err := client.CreateResource(ctx, &apigateway.CreateResourceInput{
		RestApiId: newApi.Id,
		ParentId:  newApi.RootResourceId,
		PathPart:  &wildcardPath,
	})
	if err != nil {
		return fmt.Errorf("cannot create wildcard handler: %w", err)
	}

	// handle requests received for the wildcard handler
	_, err = client.PutMethod(ctx, &apigateway.PutMethodInput{
		RestApiId:         newApi.Id,
		ResourceId:        wildcardHandler.Id,
		HttpMethod:        &allowedHttpMethod,
		AuthorizationType: &authorizationType,
		RequestParameters: params,
	})
	if err != nil {
		return fmt.Errorf("cannot create wildcard method input: %w", err)
	}

	_, err = client.PutIntegration(ctx, &apigateway.PutIntegrationInput{
		RestApiId:             newApi.Id,
		ResourceId:            wildcardHandler.Id,
		Type:                  types.IntegrationTypeHttpProxy,
		HttpMethod:            &allowedHttpMethod,
		IntegrationHttpMethod: &allowedHttpMethod,
		Uri:                   &ag.Site,
		ConnectionType:        types.ConnectionTypeInternet,
		RequestParameters:     integrationParams,
	})
	if err != nil {
		return fmt.Errorf("cannot integrate wildcard method: %w", err)
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

// route the original request through a proxy
func (ag *apiGateway) Reroute(request *http.Request) {
	// use a random endpoints as proxy
	ep := ag.Endpoints[rand.Intn(len(ag.Endpoints)-1)]

	// replace request's url with proxy endpoint and replace Host header
	_, site, found := strings.Cut(request.RequestURI, "://")
	if !found {
		return
	}

	proxyUrl, err := url.Parse("https://" + ep + "/ProxyStage" + site)
	if err != nil {
		return
	}
	request.URL = proxyUrl
	request.Header.Add("Host", ep)

	// generate X-Forwarded-For header if original request does not have it
	// and move original X-Forwarded-For to a temp header
	val := request.Header.Get("X-Forwarded-For")
	if val == "" {
		randIp := randomIpv4().String()
		request.Header.Add("X-Forwarded-For-Temp", randIp)
	} else {
		request.Header.Add("X-Forwarded-For-Temp", val)
	}
	request.Header.Del("X-Forwarded-For")
}

func (ag *apiGateway) GetGateways(region string, ctx context.Context) (*[]types.RestApi, error) {
	result := []types.RestApi{}
	defaultPosition := ""
	var defaultLimit int32 = 500
	complete := false

	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		panic(err)
	}
	cfg.Region = region
	fmt.Println(cfg.Region)
	client := apigateway.NewFromConfig(cfg)

	for !complete {
		inputParams := apigateway.GetRestApisInput{
			Limit: &defaultLimit,
		}
		if defaultPosition != "" {
			inputParams.Position = &defaultPosition
		}
		response, err := client.GetRestApis(ctx, &inputParams)
		if err != nil {
			return &result, fmt.Errorf("cannot get rest apis: %w", err)
		}

		if response != nil && response.Position != nil {
			result = append(result, response.Items...)
			defaultPosition = *response.Position
		} else {
			// no pagination or end of pagination
			result = append(result, response.Items...)
			complete = true
		}
	}

	return &result, nil
}

func (ag *apiGateway) GetEndpoints(region string, ctx context.Context) (*[]string, error) {
	apis, err := ag.GetGateways(region, ctx)
	if err != nil {
		return &[]string{}, err
	}

	endpoints := []string{}
	for _, i := range *apis {
		endpoints = append(endpoints, fmt.Sprintf("%s.execute-api.%s.amazonaws.com", *i.Id, region))
	}

	return &endpoints, nil
}

// delete all gateways from all regions
func (ag *apiGateway) DeleteGateways(region string, ctx context.Context) (*[]string, error) {

	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		panic(err)
	}
	cfg.Region = region
	client := apigateway.NewFromConfig(cfg)

	deletedIds := []string{}
	apis, err := ag.GetGateways(region, ctx)
	if err != nil {
		return &deletedIds, err
	}
	for _, api := range *apis {
		if _, err := client.DeleteRestApi(ctx, &apigateway.DeleteRestApiInput{
			RestApiId: api.Id,
		}); err != nil {
			return &deletedIds, err
		}
		deletedIds = append(deletedIds, *api.Id)

	}

	return &deletedIds, nil
}

func main() {
	gateway, err := NewApiGateway("https://mangahere.cc", "mangahere")
	if err != nil {
		panic(err)
	}

	for _, re := range DEFAULT_REGIONS {
		endpoints, err := gateway.GetEndpoints(re, context.TODO())
		if err != nil {
			fmt.Println(err)
		}
		fmt.Println(endpoints)
	}
}
