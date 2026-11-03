#include <stdio.h>
#include <string.h>
#include <time.h>
#include <sys/time.h>

#include "esp_log.h"
#include "esp_eap_client.h"
#include "esp_err.h"
#include "esp_event.h"
#include "esp_netif.h"
#include "esp_wifi.h"
#include "esp_sntp.h"
#include "nvs_flash.h"

#include "esp_adc/adc_oneshot.h"
#include "mqtt_client.h"

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "freertos/event_groups.h"

#include "cJSON.h"

#include "secrets.h"

// ---------- Configuration ----------
#define MQTT_TOPIC      "telemetry/home/light/data"
#define DEVICE_ID       "light_sensor_01"

#define SENSOR_ADC_UNIT       ADC_UNIT_1
#define SENSOR_ADC_CHANNEL    ADC_CHANNEL_6  // GPIO34

#define PUBLISH_PERIOD_MS     60000

// Temporary coefficient
#define LUX_PER_ADC_COUNT     1.0f

// ---------- State ----------

static const char *TAG = "light_sensor";

static EventGroupHandle_t event_group;
static esp_mqtt_client_handle_t mqtt_client;

#define WIFI_CONNECTED_BIT BIT0
#define MQTT_CONNECTED_BIT BIT1
#define TIME_SYNCED_BIT    BIT2

static int serial_number = 0;

// ---------- Sensor ----------

static adc_oneshot_unit_handle_t adc_handle;

static void sensor_init(void)
{
    const adc_oneshot_unit_init_cfg_t unit_config = {
        .unit_id = SENSOR_ADC_UNIT,
    };

    ESP_ERROR_CHECK(
        adc_oneshot_new_unit(&unit_config, &adc_handle)
    );

    const adc_oneshot_chan_cfg_t channel_config = {
        .atten = ADC_ATTEN_DB_12,
        .bitwidth = ADC_BITWIDTH_DEFAULT,
    };

    ESP_ERROR_CHECK(
        adc_oneshot_config_channel(
            adc_handle,
            SENSOR_ADC_CHANNEL,
            &channel_config
        )
    );

    ESP_LOGI(TAG, "TEMT6000 initialized on GPIO34");
}

static int sensor_read_raw(void)
{
    int value = 0;

    esp_err_t err = adc_oneshot_read(
        adc_handle,
        SENSOR_ADC_CHANNEL,
        &value
    );

    if (err != ESP_OK) {
        ESP_LOGE(TAG, "ADC read failed: %s", esp_err_to_name(err));
        return -1;
    }

    return value;
}

static float raw_to_lux(int raw)
{
    // Placeholder only: replace with a calibrated conversion.
    return (float)raw * LUX_PER_ADC_COUNT;
}

// ---------- Time synchronization ----------

static void time_sync_notification_cb(struct timeval *tv)
{
    ESP_LOGI(TAG, "Time synchronized");
    xEventGroupSetBits(event_group, TIME_SYNCED_BIT);
}

static void start_time_sync(void)
{
    esp_sntp_setoperatingmode(SNTP_OPMODE_POLL);
    esp_sntp_setservername(0, "pool.ntp.org");
    esp_sntp_set_time_sync_notification_cb(time_sync_notification_cb);
    esp_sntp_init();
}

// ---------- MQTT ----------

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
            xEventGroupSetBits(event_group, MQTT_CONNECTED_BIT);
            break;

        case MQTT_EVENT_DISCONNECTED:
            ESP_LOGW(TAG, "Disconnected from MQTT broker");
            xEventGroupClearBits(event_group, MQTT_CONNECTED_BIT);
            break;

        case MQTT_EVENT_ERROR:
            ESP_LOGE(TAG, "MQTT connection error");
            break;

        default:
            break;
    }
}

static void mqtt_start(void)
{
    const esp_mqtt_client_config_t mqtt_cfg = {
        .broker.address.uri = MQTT_BROKER_URI,
    };

    mqtt_client = esp_mqtt_client_init(&mqtt_cfg);

    ESP_ERROR_CHECK(
        esp_mqtt_client_register_event(
            mqtt_client,
            ESP_EVENT_ANY_ID,
            mqtt_event_handler,
            NULL
        )
    );

    ESP_ERROR_CHECK(esp_mqtt_client_start(mqtt_client));
}

// ---------- Wi-Fi ----------

static void wifi_event_handler(
    void *arg,
    esp_event_base_t event_base,
    int32_t event_id,
    void *event_data
)
{
    (void)arg;
    (void)event_data;

    if (event_base == WIFI_EVENT &&
        event_id == WIFI_EVENT_STA_START) {
        esp_wifi_connect();
    } else if (event_base == WIFI_EVENT &&
               event_id == WIFI_EVENT_STA_DISCONNECTED) {
        xEventGroupClearBits(event_group, WIFI_CONNECTED_BIT);
        ESP_LOGW(TAG, "Wi-Fi disconnected; reconnecting");
        esp_wifi_connect();
    } else if (event_base == IP_EVENT &&
               event_id == IP_EVENT_STA_GOT_IP) {
        ESP_LOGI(TAG, "Wi-Fi connected; IP address acquired");
        xEventGroupSetBits(event_group, WIFI_CONNECTED_BIT);
    }
}

static void wifi_init(void)
{
    ESP_ERROR_CHECK(esp_netif_init());
    ESP_ERROR_CHECK(esp_event_loop_create_default());

    esp_netif_create_default_wifi_sta();

    const wifi_init_config_t cfg = WIFI_INIT_CONFIG_DEFAULT();
    ESP_ERROR_CHECK(esp_wifi_init(&cfg));

    ESP_ERROR_CHECK(
        esp_event_handler_register(
            WIFI_EVENT,
            ESP_EVENT_ANY_ID,
            wifi_event_handler,
            NULL
        )
    );

    ESP_ERROR_CHECK(
        esp_event_handler_register(
            IP_EVENT,
            IP_EVENT_STA_GOT_IP,
            wifi_event_handler,
            NULL
        )
    );

    wifi_config_t wifi_config = {0};

    strlcpy(
        (char *)wifi_config.sta.ssid,
        WIFI_SSID,
        sizeof(wifi_config.sta.ssid)
    );

    ESP_ERROR_CHECK(esp_wifi_set_mode(WIFI_MODE_STA));
    ESP_ERROR_CHECK(esp_wifi_set_config(WIFI_IF_STA, &wifi_config));

    ESP_ERROR_CHECK(esp_wifi_sta_enterprise_enable());

    ESP_ERROR_CHECK(
        esp_eap_client_set_identity(
            (const unsigned char *)WIFI_USERNAME,
            strlen(WIFI_USERNAME)
        )
    );

    ESP_ERROR_CHECK(
        esp_eap_client_set_username(
            (const unsigned char *)WIFI_USERNAME,
            strlen(WIFI_USERNAME)
        )
    );

    ESP_ERROR_CHECK(
        esp_eap_client_set_password(
            (const unsigned char *)WIFI_PASSWORD,
            strlen(WIFI_PASSWORD)
        )
    );

    ESP_ERROR_CHECK(esp_wifi_sta_enterprise_enable());

    ESP_ERROR_CHECK(esp_wifi_start());
}

// ---------- Telemetry ----------

static void publish_telemetry(void)
{
    int raw = sensor_read_raw();

    if (raw < 0) {
        return;
    }

    float lux = raw_to_lux(raw);

    time_t now;
    struct tm timeinfo;

    time(&now);
    gmtime_r(&now, &timeinfo);

    char timestamp[32];

    strftime(
        timestamp,
        sizeof(timestamp),
        "%Y-%m-%dT%H:%M:%SZ",
        &timeinfo
    );

    cJSON *root = cJSON_CreateObject();

    if (root == NULL) {
        ESP_LOGE(TAG, "Failed to create JSON");
        return;
    }

    cJSON_AddStringToObject(root, "device_id", DEVICE_ID);
    cJSON_AddNumberToObject(root, "illuminance", lux);
    cJSON_AddStringToObject(root, "unit", "lux");
    cJSON_AddNumberToObject(root, "serial", ++serial_number);
    cJSON_AddStringToObject(root, "timestamp", timestamp);

    char *payload = cJSON_PrintUnformatted(root);

    if (payload == NULL) {
        ESP_LOGE(TAG, "Failed to serialize JSON");
        cJSON_Delete(root);
        return;
    }

    int msg_id = esp_mqtt_client_publish(
        mqtt_client,
        MQTT_TOPIC,
        payload,
        0,
        1,
        0
    );

    if (msg_id >= 0) {
        ESP_LOGI(TAG, "Published to %s: %s", MQTT_TOPIC, payload);
    } else {
        ESP_LOGE(TAG, "MQTT publish failed");
    }

    cJSON_free(payload);
    cJSON_Delete(root);
}

// ---------- Application ----------

static void telemetry_task(void *arg)
{
    (void)arg;

    // Wait for a connection to the broker and synchronized time.
    xEventGroupWaitBits(
        event_group,
        MQTT_CONNECTED_BIT | TIME_SYNCED_BIT,
        pdFALSE,
        pdTRUE,
        portMAX_DELAY
    );

    while (1) {
        publish_telemetry();
        vTaskDelay(pdMS_TO_TICKS(PUBLISH_PERIOD_MS));
    }
}

void app_main(void)
{
    esp_err_t ret = nvs_flash_init();

    if (ret == ESP_ERR_NVS_NO_FREE_PAGES ||
        ret == ESP_ERR_NVS_NEW_VERSION_FOUND) {
        ESP_ERROR_CHECK(nvs_flash_erase());
        ret = nvs_flash_init();
    }

    ESP_ERROR_CHECK(ret);

    event_group = xEventGroupCreate();

    if (event_group == NULL) {
        ESP_LOGE(TAG, "Failed to create event group");
        return;
    }

    sensor_init();

    wifi_init();
    start_time_sync();

    // Start MQTT only after Wi-Fi is connected.
    xEventGroupWaitBits(
        event_group,
        WIFI_CONNECTED_BIT,
        pdFALSE,
        pdTRUE,
        portMAX_DELAY
    );

    mqtt_start();

    if (xTaskCreate(
            telemetry_task,
            "telemetry",
            4096,
            NULL,
            5,
            NULL
        ) != pdPASS) {
        ESP_LOGE(TAG, "Failed to create telemetry task");
    }
}