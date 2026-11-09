#include "mqtt_manager.h"

#include "esp_log.h"
#include "mqtt_client.h"

#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"

#include "secrets.h"


static const char *TAG = "mqtt_manager";

static esp_mqtt_client_handle_t mqtt_client;

static EventGroupHandle_t mqtt_event_group;

#define MQTT_CONNECTED_BIT BIT0


static void mqtt_event_handler(
    void *handler_args,
    esp_event_base_t base,
    int32_t event_id,
    void *event_data
)
{
    (void)handler_args;
    (void)base;
    (void)event_data;

    switch (event_id) {

    case MQTT_EVENT_CONNECTED:

        ESP_LOGI(TAG, "Connected to MQTT broker");

        xEventGroupSetBits(
            mqtt_event_group,
            MQTT_CONNECTED_BIT
        );

        break;

    case MQTT_EVENT_DISCONNECTED:

        ESP_LOGW(TAG, "Disconnected from MQTT broker");

        xEventGroupClearBits(
            mqtt_event_group,
            MQTT_CONNECTED_BIT
        );

        break;

    case MQTT_EVENT_ERROR:

        ESP_LOGE(TAG, "MQTT connection error");

        break;

    default:
        break;
    }
}


esp_err_t mqtt_manager_init(void)
{
    mqtt_event_group = xEventGroupCreate();

    if (mqtt_event_group == NULL) {
        ESP_LOGE(TAG, "Failed to create event group");

        return ESP_ERR_NO_MEM;
    }

    const esp_mqtt_client_config_t mqtt_cfg = {
        .broker.address.uri = MQTT_BROKER_URI,
    };

    mqtt_client = esp_mqtt_client_init(&mqtt_cfg);

    if (mqtt_client == NULL) {
        ESP_LOGE(TAG, "Failed to create MQTT client");

        vEventGroupDelete(mqtt_event_group);
        mqtt_event_group = NULL;

        return ESP_FAIL;
    }

    esp_err_t err = esp_mqtt_client_register_event(
        mqtt_client,
        ESP_EVENT_ANY_ID,
        mqtt_event_handler,
        NULL
    );

    if (err != ESP_OK) {
        return err;
    }

    return esp_mqtt_client_start(mqtt_client);
}


void mqtt_manager_wait_connected(void)
{
    if (mqtt_event_group == NULL) {
        ESP_LOGE(
            TAG,
            "MQTT manager is not initialized"
        );

        return;
    }

    xEventGroupWaitBits(
        mqtt_event_group,
        MQTT_CONNECTED_BIT,
        pdFALSE,
        pdTRUE,
        portMAX_DELAY
    );
}


bool mqtt_manager_is_connected(void)
{
    if (mqtt_event_group == NULL) {
        return false;
    }

    EventBits_t bits = xEventGroupGetBits(
        mqtt_event_group
    );

    return (bits & MQTT_CONNECTED_BIT) != 0;
}


int mqtt_manager_publish(
    const char *topic,
    const char *data,
    int qos,
    int retain
)
{
    if (mqtt_client == NULL) {
        ESP_LOGE(TAG, "MQTT client is not initialized");

        return -1;
    }

    if (!mqtt_manager_is_connected()) {
        ESP_LOGW(TAG, "Cannot publish: MQTT is disconnected");

        return -1;
    }

    return esp_mqtt_client_publish(
        mqtt_client,
        topic,
        data,
        0,
        qos,
        retain
    );
}