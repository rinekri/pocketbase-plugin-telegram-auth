package pocketbase_plugin_telegram_auth

import (
	"errors"
	"fmt"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	pbForms "github.com/pocketbase/pocketbase/forms"
	"github.com/pocketbase/pocketbase/tools/auth"
	"github.com/pocketbase/pocketbase/tools/search"
	"github.com/rinekri/pocketbase-plugin-telegram-auth/forms"
)

// Options defines optional struct to customize the default plugin behavior.
type Options struct {
	// BotToken is a Telegram bot token.
	// You can get it from @BotFather.
	BotToken string

	// CollectionKey is a collection key (name or id) for PocketBase auth collection.
	CollectionKey string
}

type PluginContext struct {
	app        core.App
	options    *Options
	collection *core.Collection
}

// Validate plugin options. Return error if some option is invalid.
func (p *PluginContext) Validate() error {
	if p.options == nil {
		return fmt.Errorf("options is required")
	}

	if p.options.BotToken == "" {
		return fmt.Errorf("bot token is required")
	}

	if p.options.CollectionKey == "" {
		return fmt.Errorf("collection key is required")
	}

	return nil
}

// GetCollection returns PocketBase collection object for collection with name or id from options.CollectionKey.
func (p *PluginContext) GetCollection() (*core.Collection, error) {
	// If collection object stored in plugin - return it
	if p.collection != nil {
		return p.collection, nil
	}

	// If no collection object - find it, store and return
	if collection, err := p.app.FindCollectionByNameOrId(p.options.CollectionKey); err != nil {
		return nil, err
	} else {
		p.collection = collection
		return collection, nil
	}
}

// GetForm returns Telegram login form for collection with name or id from options.CollectionKey.
func (p *PluginContext) GetForm(optAuthRecord *core.Record) (*forms.RecordTelegramLogin, error) {
	collection, findCollectionErr := p.GetCollection()
	if findCollectionErr != nil {
		return nil, findCollectionErr
	}
	if collection.Type != core.CollectionTypeAuth {
		return nil, errors.New("Wrong collection type. " + p.options.CollectionKey + " should be auth collection")
	}

	return forms.NewRecordTelegramLogin(p.app, p.options.BotToken, collection, optAuthRecord), nil
}

// AuthByTelegramData returns auth record and auth user by Telegram data.
func (p *PluginContext) AuthByTelegramData(tgData forms.TelegramData) (*core.Record, *auth.AuthUser, error) {
	form, err := p.GetForm(nil)
	if err != nil {
		return nil, nil, err
	}

	return form.SubmitWithTelegramData(&tgData)
}

// MustRegister is a helper function to register plugin and panic if error occurred.
func MustRegister(app core.App, options *Options) *PluginContext {
	if p, err := Register(app, options); err != nil {
		panic(err)
	} else {
		return p
	}
}

// Register plugin in PocketBase app.
func Register(app core.App, options *Options) (*PluginContext, error) {
	p, err := CreateAndValidatePluginContext(app, options)

	if err != nil {
		return p, err
	}

	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		e.Router.POST(
			// TODO: Rmove collection key and collections 
			"/api/collections/"+p.options.CollectionKey+"/auth-with-telegram",
			p.AuthHandler,
		)

		return e.Next()
	})

	return p, nil
}

func CreateAndValidatePluginContext(app core.App, options *Options) (*PluginContext, error) {
	p := &PluginContext{app: app}

	// Set default options
	if options != nil {
		p.options = options
	} else {
		p.options = &Options{}
	}

	// Validate options
	if err := p.Validate(); err != nil {
		return p, err
	}
	return p, nil
}

func (p *PluginContext) AuthHandler(c *core.RequestEvent) error {
	collection, findCollectionErr := p.GetCollection()
	if findCollectionErr != nil {
		return apis.NewNotFoundError("Collection not found", findCollectionErr)
	}

	var fallbackAuthRecord *core.Record
	loggedAuthRecord := c.Auth
	if loggedAuthRecord != nil && loggedAuthRecord.Collection().Id == collection.Id {
		fallbackAuthRecord = loggedAuthRecord
	}

	form, getFormErr := p.GetForm(fallbackAuthRecord)
	if getFormErr != nil {
		return apis.NewBadRequestError(getFormErr.Error(), getFormErr)
	}
	if readErr := c.BindBody(form); readErr != nil {
		return apis.NewBadRequestError("An error occurred while loading the submitted data.", readErr)
	}

	record, authData, submitErr := form.Submit(
		func(createForm *pbForms.RecordUpsert, authRecord *core.Record, authUser *auth.AuthUser) error {
			return createForm.DrySubmit(func(txApp core.App, drySavedRecord *core.Record) error {
				requestInfo, _ := c.RequestInfo()
				requestInfo.Body = form.CreateData

				createRuleFunc := func(q *dbx.SelectQuery) error {
					admin := c.Auth

					if admin != nil && admin.IsSuperuser() {
						return nil // either admin or the rule is empty
					}

					if collection.CreateRule == nil {
						return errors.New("Only admins can create new accounts with OAuth2")
					}

					if *collection.CreateRule != "" {
						resolver := core.NewRecordFieldResolver(c.App, collection, requestInfo, true)
						if expr, err := search.FilterData(*collection.CreateRule).BuildExpr(resolver); err != nil {
							return err
						} else {
							if updateQueryError := resolver.UpdateQuery(q); updateQueryError != nil {
								return updateQueryError
							}
							q.AndWhere(expr)
						}
					}

					return nil
				}

				if _, err := txApp.FindRecordById(collection.Id, authRecord.Id, createRuleFunc); err != nil {
					return fmt.Errorf("Failed create rule constraint: %w", err)
				}

				return nil
			})
		})

	if submitErr != nil {
		return apis.NewBadRequestError("Failed to authenticate.", submitErr)
	}

	return apis.RecordAuthResponse(c, record, core.MFAMethodOAuth2, authData)
}


func (p *PluginContext) AuthHandlerTelegramData(
	c *core.RequestEvent,
	tgData *forms.TelegramData,
) error {
	collection, findCollectionErr := p.GetCollection()
	if findCollectionErr != nil {
		return apis.NewNotFoundError("Collection not found", findCollectionErr)
	}

	var fallbackAuthRecord *core.Record
	loggedAuthRecord := c.Auth
	if loggedAuthRecord != nil && loggedAuthRecord.Collection().Id == collection.Id {
		fallbackAuthRecord = loggedAuthRecord
	}

	form, getFormErr := p.GetForm(fallbackAuthRecord)
	if getFormErr != nil {
		return apis.NewBadRequestError(getFormErr.Error(), getFormErr)
	}
	if readErr := c.BindBody(form); readErr != nil {
		return apis.NewBadRequestError("An error occurred while loading the submitted data.", readErr)
	}

	record, authData, submitErr := form.SubmitWithTelegramData(
		tgData,
		func(createForm *pbForms.RecordUpsert, authRecord *core.Record, authUser *auth.AuthUser) error {
			return createForm.DrySubmit(func(txApp core.App, drySavedRecord *core.Record) error {
				requestInfo, _ := c.RequestInfo()
				requestInfo.Body = form.CreateData

				createRuleFunc := func(q *dbx.SelectQuery) error {
					admin := c.Auth

					if admin != nil && admin.IsSuperuser() {
						return nil // either admin or the rule is empty
					}

					if collection.CreateRule == nil {
						return errors.New("Only admins can create new accounts with OAuth2")
					}

					if *collection.CreateRule != "" {
						resolver := core.NewRecordFieldResolver(c.App, collection, requestInfo, true)
						if expr, err := search.FilterData(*collection.CreateRule).BuildExpr(resolver); err != nil {
							return err
						} else {
							if updateQueryError := resolver.UpdateQuery(q); updateQueryError != nil {
								return updateQueryError
							}
							q.AndWhere(expr)
						}
					}

					return nil
				}

				if _, err := txApp.FindRecordById(collection.Id, authRecord.Id, createRuleFunc); err != nil {
					return fmt.Errorf("Failed create rule constraint: %w", err)
				}

				return nil
			})
		})

	if submitErr != nil {
		return apis.NewBadRequestError("Failed to authenticate.", submitErr)
	}

	return apis.RecordAuthResponse(c, record, core.MFAMethodOAuth2, authData)
}