# Google Search Engine Results API

## API Uptime

**99.731%**

The `/search` API endpoint allows you to scrape the results from the Google search engine via our SerpApi service. Head to the playground for a live and interactive demo. You can query `https://serpapi.com/search` using a GET request.

## API Parameters

### Search Query

| Parameter | Status       | Description                                                                                                                                                                                                                 |
| --------- | ------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `q`       | **Required** | Defines the query you want to search. You can use anything that you would use in a regular Google search. e.g. `inurl:`, `site:`, `intitle:`. We also support advanced search query parameters such as `as_dt` and `as_eq`. |

### Geographic Location

| Parameter  | Status   | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| ---------- | -------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `location` | Optional | Defines where you want the search to originate. If several locations match the location requested, we'll pick the most popular one. It is recommended to specify the location at the city level to simulate a real user’s search. If `location` is omitted, the search may take on the location of the proxy. **Note:** The `location` and `uule` parameters can't be used together. When only the `location` parameter is set, Google may still take into account the proxy’s country, which can influence some results. For more consistent country-specific filtering, use the `gl` parameter alongside `location`. |
| `uule`     | Optional | The Google encoded location you want to use for the search. The `uule` and `location` parameters can't be used together.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               |

### Advanced Google Parameters

| Parameter | Status   | Description                                                                              |
| --------- | -------- | ---------------------------------------------------------------------------------------- |
| `ludocid` | Optional | Defines the Google CID (customer identifier) of a place.                                 |
| `lsig`    | Optional | A parameter that you might have to use to force the knowledge graph map view to show up. |
| `kgmid`   | Optional | Defines the ID (KGMID) of the Google Knowledge Graph listing you want to scrape.         |
| `si`      | Optional | Defines the cached search parameters of the Google Search you want to scrape.            |
| `ibp`     | Optional | Responsible for rendering layouts and expansions for some elements.                      |
| `uds`     | Optional | Enables filtering of the search. It's a string provided by Google as a filter.           |

### Localization

| Parameter       | Status   | Description                                                                                                           |
| --------------- | -------- | --------------------------------------------------------------------------------------------------------------------- | ----------------- |
| `google_domain` | Optional | Defines the Google domain to use. It defaults to `google.com`.                                                        |
| `gl`            | Optional | Defines the country to use for the Google search. It's a two-letter country code (e.g., `us`, `uk`, `fr`).            |
| `hl`            | Optional | Defines the language to use for the Google search. It's a two-letter language code (e.g., `en`, `es`, `fr`).          |
| `cr`            | Optional | Defines one or multiple countries to limit the search to. It uses `country{two-letter upper-case country code}` and ` | ` as a delimiter. |
| `lr`            | Optional | Defines one or multiple languages to limit the search to. It uses `lang_{two-letter language code}` and `             | ` as a delimiter. |

### Advanced Filters

| Parameter | Status   | Description                                                                                                       |
| --------- | -------- | ----------------------------------------------------------------------------------------------------------------- |
| `tbs`     | Optional | (to be searched) Defines advanced search parameters that aren't possible in the regular query field.              |
| `safe`    | Optional | Defines the level of filtering for adult content. Can be set to `active` or `off`.                                |
| `nfpr`    | Optional | Defines the exclusion of results from an auto-corrected query. Set to `1` to exclude, `0` to include.             |
| `filter`  | Optional | Defines if the filters for 'Similar Results' and 'Omitted Results' are on or off. Set to `1` for on, `0` for off. |

### Search Type

| Parameter | Status   | Description                                                                                                            |
| --------- | -------- | ---------------------------------------------------------------------------------------------------------------------- |
| `tbm`     | Optional | (to be matched) Defines the type of search you want to do (e.g., `isch` for Images, `vid` for Videos, `nws` for News). |

### Pagination

| Parameter | Status   | Description                                                                                 |
| --------- | -------- | ------------------------------------------------------------------------------------------- |
| `start`   | Optional | Defines the result offset. It skips the given number of results and is used for pagination. |
| `num`     | Optional | Defines the maximum number of results to return (e.g., `10`, `40`, `100`).                  |

### Serpapi Parameters

| Parameter         | Status       | Description                                                                                                   |
| ----------------- | ------------ | ------------------------------------------------------------------------------------------------------------- |
| `engine`          | Optional     | Set to `google` (default) to use the Google API engine.                                                       |
| `device`          | Optional     | Defines the device to use. Can be `desktop` (default), `tablet`, or `mobile`.                                 |
| `no_cache`        | Optional     | Forces SerpApi to fetch the Google results even if a cached version is present.                               |
| `async`           | Optional     | Defines the way you want to submit your search to SerpApi (`false` for synchronous, `true` for asynchronous). |
| `zero_trace`      | Optional     | **Enterprise only.** Enables ZeroTrace mode, which skips storing search parameters and data.                  |
| `api_key`         | **Required** | Defines the SerpApi private key to use.                                                                       |
| `output`          | Optional     | Defines the final output you want. Can be `json` (default) or `html`.                                         |
| `json_restrictor` | Optional     | Defines the fields you want to restrict in the outputs for smaller, faster responses.                         |

**Note on Search Queries using the `num` parameter:** Due to Google's new Knowledge Graph layout, the `num` parameter may be ignored for the first page of results in many searches. When the `start` parameter is used and set to 1 or higher, the `num` parameter works as expected.

## API Results

### JSON Results

JSON output includes structured data for organic results, local results, ad results, the knowledge graph, direct answer boxes, and more. A search status is accessible through `search_metadata.status`.

### HTML Results

HTML output is useful to debug JSON results or support features not yet supported by SerpApi. It gives you the raw HTML results from Google.

## API Examples

### Example with `q`: Coffee parameter

**GET Request**

```
https://serpapi.com/search.json?engine=google&q=Coffee
```

**Code to Integrate (Ruby)**```ruby
require "serpapi"

client = SerpApi::Client.new(
engine: "google",
q: "Coffee",
api_key: "YOUR_API_KEY"
)

results = client.search
organic_results = results[:organic_results]

````

**JSON Example**
```json
{
  "search_metadata": {
    "id": "61afb3ace7d08a685b3bcbb1",
    "status": "Success",
    "json_endpoint": "https://serpapi.com/searches/c292c1c1fe17fc58/61afb3ace7d08a685b3bcbb1.json",
    "created_at": "2021-12-07 19:19:08 UTC",
    "processed_at": "2021-12-07 19:19:08 UTC",
    "google_url": "https://www.google.com/search?q=coffee&oq=coffee&uule=w+CAIQICIaQXVzdGluLFRleGFzLFVuaXRlZCBTdGF0ZXM&hl=en&gl=us&sourceid=chrome&ie=UTF-8",
    "raw_html_file": "https://serpapi.com/searches/c292c1c1fe17fc58/61afb3ace7d08a685b3bcbb1.html",
    "total_time_taken": 1.52
  },
  "search_parameters": {
    "engine": "google",
    "q": "coffee",
    "location_requested": "Austin, Texas, United States",
    "location_used": "Austin,Texas,United States",
    "google_domain": "google.com",
    "hl": "en",
    "gl": "us",
    "device": "desktop"
  }
}
````

### More complex examples with multiple optional parameters

The URL below fetches the second page of results for "Fresh Bagels" in Seattle, using `google.com` in English, with the adult content filter on.

**GET Request**

````
https://serpapi.com/search.json?engine=google&q=Fresh+Bagels&location=Seattle-Tacoma,+WA,+Washington,+United+States&hl=en&gl=us&google_domain=google.com&num=10&start=10&safe=active```

**Code to Integrate (Ruby)**
```ruby
require "serpapi"

client = SerpApi::Client.new(
  engine: "google",
  q: "Fresh Bagels",
  location: "Seattle-Tacoma, WA, Washington, United States",
  hl: "en",
  gl: "us",
  google_domain: "google.com",
  num: "10",
  start: "10",
  safe: "active",
  api_key: "YOUR_API_KEY"
)

results = client.search
organic_results = results[:organic_results]
````
