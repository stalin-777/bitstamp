package bitstamp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/crxfoz/webclient"
	"github.com/google/uuid"
)

// From API docs: Should you receive the error response 'Order could not be placed' when trying to place an order, please retry order placement.

var (
	ErrStatus = errors.New("incorrect status code")
)

type ErrorResult struct {
	Status string      `json:"status"`
	Reason interface{} `json:"reason"`
	Code   string      `json:"code"`
}

func (er ErrorResult) Error() string {
	return fmt.Sprintf("api error: %s", er.Reason)
}

type PrivateClient struct {
	APIKey    string
	SecretKey string
	client    *webclient.Webclient
	observer  OrderObserver
}

func NewPrivateClient(apiKey string, secretKey string, observer OrderObserver) *PrivateClient {
	return &PrivateClient{
		APIKey:    apiKey,
		SecretKey: secretKey,
		client: webclient.Config{
			Timeout:        time.Second * 10,
			UseKeepAlive:   false,
			FollowRedirect: false,
		}.New(),
		observer: observer,
	}
}

func (pc *PrivateClient) privateRequest(path string, params map[string]string) (string, error) {
	ts := time.Now().Add(time.Second * 10).Unix()
	nonce, err := uuid.NewUUID()
	if err != nil {
		return "", err
	}

	// line from bitstamp API official docs
	// it doesn't work without this line and with UnixNano
	ts *= 1000

	values := url.Values{}
	for k, v := range params {
		values.Add(k, v)
	}

	// if request body is empty, shouldn't pass Content-Type
	contentType := ""

	if len(params) > 0 {
		contentType = "application/x-www-form-urlencoded"
	}

	// Bitstamp API v2 auth method: https://www.bitstamp.net/api/
	msg := fmt.Sprintf("BITSTAMP %s"+
		"POST"+
		"www.bitstamp.net"+
		"%s"+
		"%s"+
		"%s"+
		"%d"+
		"v2"+
		"%s", pc.APIKey, path, contentType, nonce, ts, values.Encode())

	h := hmac.New(sha256.New, []byte(pc.SecretKey))
	if _, err := h.Write([]byte(msg)); err != nil {
		return "", err
	}

	sign := h.Sum(nil)

	headers := map[string]string{
		"X-Auth":           fmt.Sprintf("BITSTAMP %s", pc.APIKey),
		"X-Auth-Signature": hex.EncodeToString(sign),
		"X-Auth-Nonce":     nonce.String(),
		"X-Auth-Timestamp": fmt.Sprintf("%d", ts),
		"X-Auth-Version":   "v2",
	}

	req := pc.client.Post(fmt.Sprintf("https://www.bitstamp.net%s", path)).SetHeaders(headers)

	for k, v := range params {
		req.SendParam(k, v)
	}

	_, body, err := req.Do()
	if err != nil {
		return "", err
	}

	// parsing errors
	var errBody ErrorResult

	if err := json.Unmarshal([]byte(body), &errBody); err == nil && errBody.Status == "error" {
		return "", errBody
	}

	return body, nil
}

func (pc *PrivateClient) GetBalances() (BalanceResult, error) {
	resp, err := pc.privateRequest("/api/v2/balance/", nil)
	if err != nil {
		return BalanceResult{}, err
	}

	var balances BalanceResult

	if err := json.Unmarshal([]byte(resp), &balances); err != nil {
		return BalanceResult{}, err
	}

	return balances, nil
}

func (pc *PrivateClient) GetTransactions() ([]TransactionResult, error) {
	resp, err := pc.privateRequest("/api/v2/user_transactions/", nil)
	if err != nil {
		return nil, err
	}

	var transactions []TransactionResult

	if err := json.Unmarshal([]byte(resp), &transactions); err != nil {
		return nil, err
	}

	return transactions, nil
}

func (pc *PrivateClient) GetOpenOrders() ([]OpenOrderResult, error) {
	resp, err := pc.privateRequest("/api/v2/open_orders/all/", nil)
	if err != nil {
		return nil, err
	}

	var orders []OpenOrderResult

	if err := json.Unmarshal([]byte(resp), &orders); err != nil {
		return nil, err
	}

	return orders, nil
}

func (pc *PrivateClient) GetOrderStatus(id string) (OrderStatusResult, error) {
	resp, err := pc.privateRequest("/api/v2/order_status/", map[string]string{"id": id})
	if err != nil {
		return OrderStatusResult{}, err
	}

	var status OrderStatusResult

	if err := json.Unmarshal([]byte(resp), &status); err != nil {
		return OrderStatusResult{}, err
	}

	return status, nil
}

func (pc *PrivateClient) CancelOrder(id string) (OrderCancelResult, error) {
	resp, err := pc.privateRequest("/api/v2/cancel_order/", map[string]string{"id": id})
	if err != nil {
		return OrderCancelResult{}, err
	}

	var status OrderCancelResult

	if err := json.Unmarshal([]byte(resp), &status); err != nil {
		return OrderCancelResult{}, err
	}

	return status, nil
}

// CancelAllOrders отменяет все ордера
// TODO: bitstamp возвращает список отмененных ордеров. Сейчас они не парсятся.
func (pc *PrivateClient) CancelAllOrders() (CancelAllOrdersResult, error) {
	resp, err := pc.privateRequest("/api/v2/cancel_all_orders/", nil)
	if err != nil {
		return CancelAllOrdersResult{}, err
	}

	var status CancelAllOrdersResult

	if err := json.Unmarshal([]byte(resp), &status); err != nil {
		return CancelAllOrdersResult{}, err
	}

	return status, nil
}

func (pc *PrivateClient) limitOrder(side OrderSide, order PlaceOrderRequest) (PlaceOrderResult, error) {
	path := ""

	switch side {
	case Buy:
		path = fmt.Sprintf("/api/v2/buy/%s/", order.Symbol)
	case Sell:
		path = fmt.Sprintf("/api/v2/sell/%s/", order.Symbol)
	default:
		return PlaceOrderResult{}, fmt.Errorf("wrong side")
	}

	params := make(map[string]string)

	// TODO: probably could be simplified
	// TODO: I'm not sure that 'True' is valid value but it's been written according to Bitstamp docs
	// nolint
	switch order.ExecType {
	case ExecDefault:
	case ExecDaily:
		params["daily_order"] = "True"
	case ExecFOK:
		params["fok_order"] = "True"
	case ExecIOC:
		params["ioc_order"] = "True"
	}

	params["price"] = fmt.Sprintf("%f", order.Price)
	params["amount"] = fmt.Sprintf("%f", order.Amount)

	// TODO: handle?
	_ = pc.observer.Lock()
	defer pc.observer.Unlock()

	resp, err := pc.privateRequest(path, params)
	if err != nil {
		return PlaceOrderResult{}, err
	}

	var status PlaceOrderResult

	if err := json.Unmarshal([]byte(resp), &status); err != nil {
		return PlaceOrderResult{}, err
	}

	// TODO: handle?
	_ = pc.observer.Observe(string(side), order.Symbol, status.ID)

	return status, nil
}

func (pc *PrivateClient) BuyLimitOrder(order PlaceOrderRequest) (PlaceOrderResult, error) {
	status, err := pc.limitOrder(Buy, order)
	if err != nil {
		return PlaceOrderResult{}, err
	}

	return status, nil
}

func (pc *PrivateClient) SellLimitOrder(order PlaceOrderRequest) (PlaceOrderResult, error) {
	status, err := pc.limitOrder(Sell, order)
	if err != nil {
		return PlaceOrderResult{}, err
	}

	return status, nil
}

func (pc *PrivateClient) marketOrder(side OrderSide, symbol string, amount string) (PlaceOrderResult, error) {
	path := ""

	switch side {
	case Buy:
		path = fmt.Sprintf("/api/v2/buy/market/%s/", symbol)
	case Sell:
		path = fmt.Sprintf("/api/v2/sell/market/%s/", symbol)
	default:
		return PlaceOrderResult{}, fmt.Errorf("wrong side")
	}

	// TODO: handle?
	_ = pc.observer.Lock()
	defer pc.observer.Unlock()
	resp, err := pc.privateRequest(path, map[string]string{
		"amount": amount,
	})
	if err != nil {
		return PlaceOrderResult{}, err
	}

	var status PlaceOrderResult

	if err := json.Unmarshal([]byte(resp), &status); err != nil {
		return PlaceOrderResult{}, err
	}

	// TODO: handle?
	_ = pc.observer.Observe(string(side), symbol, status.ID)

	return status, nil
}

func (pc *PrivateClient) BuyMarketOrder(symbol string, amount string) (PlaceOrderResult, error) {
	status, err := pc.marketOrder(Buy, symbol, amount)
	if err != nil {
		return PlaceOrderResult{}, err
	}

	return status, nil
}

func (pc *PrivateClient) SellMarketOrder(symbol string, amount string) (PlaceOrderResult, error) {
	status, err := pc.marketOrder(Sell, symbol, amount)
	if err != nil {
		return PlaceOrderResult{}, err
	}

	return status, nil
}

func (pc *PrivateClient) PlaceOrder(opts PlaceOrderRequest) (PlaceOrderResult, error) {
	if opts.Symbol == "" {
		return PlaceOrderResult{}, fmt.Errorf("symbol isn't specified")
	}

	if opts.Amount <= 0 {
		return PlaceOrderResult{}, fmt.Errorf("amount isn't specified")
	}

	switch opts.Type {
	case Limit:
		if opts.Price <= 0 {
			return PlaceOrderResult{}, fmt.Errorf("price can't be 0 for limit orders")
		}

		switch opts.Side {

		// limit + buy
		case Buy:
			return pc.BuyLimitOrder(opts)
		// limit + sell
		case Sell:
			return pc.SellLimitOrder(opts)

		default:
			return PlaceOrderResult{}, ErrNoSide
		}
	case Market:
		switch opts.Side {

		// market + buy
		case Buy:
			return pc.BuyMarketOrder(opts.Symbol, fmt.Sprintf("%f", opts.Amount))

		// market + sell
		case Sell:
			return pc.SellMarketOrder(opts.Symbol, fmt.Sprintf("%f", opts.Amount))

		default:
			return PlaceOrderResult{}, ErrNoSide
		}
	default:
		return PlaceOrderResult{}, fmt.Errorf("order type isn't specified")
	}
}
