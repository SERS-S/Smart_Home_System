#include <stdio.h>
#include <time.h>

#include "esp_log.h"
#include "esp_err.h"
#include "esp_sntp.h"
#include "nvs_flash.h"

#include "esp_adc/adc_oneshot.h"

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "freertos/event_groups.h"

#include "cJSON.h"

#include "wifi_manager.h"
#include "mqtt_manager.h"


// ---------- Configuration ----------

#define MQTT_TOPIC_DATA      "telemetry/home/light/data"
#define MQTT_TOPIC_INFO      "telemetry/home/light/info"

#define DEVICE_ID            "light_sensor_01"
#define VERSION              "0.1"

#define SENSOR_ADC_UNIT      ADC_UNIT_1
#define SENSOR_ADC_CHANNEL   ADC_CHANNEL_6   // GPIO34

#define VOLTAGE_ADC_CHANNEL  ADC_CHANNEL_4   // GPIO32

#define PUBLISH_PERIOD_MS    60000

#define LUX_PER_ADC_COUNT    1.0f

#define MAX_ADC_VALUE        4095
#define VOLT_PER_ADC_COUNT   (3.0f / MAX_ADC_VALUE)


// ---------- State ----------

static const char *TAG = "light_sensor";

static EventGroupHandle_t event_group;

#define TIME_SYNCED_BIT BIT0

static adc_oneshot_unit_handle_t adc_handle;

static int serial_number = 0;


// ---------- Sensor ----------

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

    ESP_ERROR_CHECK(
        adc_oneshot_config_channel(
            adc_handle,
            VOLTAGE_ADC_CHANNEL,
            &channel_config
        )
    );

    ESP_LOGI(TAG, "TEMT6000 initialized");
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
        ESP_LOGE(
            TAG,
            "ADC read failed: %s",
            esp_err_to_name(err)
        );

        return -1;
    }

    return value;
}


static float raw_to_lux(int raw)
{
    // Временная формула.
    // Для реальных lux потребуется калибровка.
    return (float)raw * LUX_PER_ADC_COUNT;
}


// ---------- Time ----------

static void time_sync_notification_cb(struct timeval *tv)
{
    (void)tv;

    ESP_LOGI(TAG, "Time synchronized");

    xEventGroupSetBits(
        event_group,
        TIME_SYNCED_BIT
    );
}


static void start_time_sync(void)
{
    esp_sntp_setoperatingmode(SNTP_OPMODE_POLL);

    esp_sntp_setservername(
        0,
        "pool.ntp.org"
    );

    esp_sntp_set_time_sync_notification_cb(
        time_sync_notification_cb
    );

    esp_sntp_init();
}


static void wait_for_time_sync(void)
{
    xEventGroupWaitBits(
        event_group,
        TIME_SYNCED_BIT,
        pdFALSE,
        pdTRUE,
        portMAX_DELAY
    );
}


// ---------- Info ----------

static float raw_to_volt(int raw)
{
    return VOLT_PER_ADC_COUNT * (float)raw;
}


static void publish_info(void)
{
    int raw = 0;

    esp_err_t err = adc_oneshot_read(
        adc_handle,
        VOLTAGE_ADC_CHANNEL,
        &raw
    );

    if (err != ESP_OK) {
        ESP_LOGE(
            TAG,
            "Voltage ADC read failed: %s",
            esp_err_to_name(err)
        );

        return;
    }

    float voltage = raw_to_volt(raw);

    cJSON *root = cJSON_CreateObject();

    if (root == NULL) {
        ESP_LOGE(TAG, "Failed to create JSON");
        return;
    }

    cJSON_AddStringToObject(
        root,
        "device_id",
        DEVICE_ID
    );

    cJSON_AddNumberToObject(
        root,
        "voltage",
        voltage
    );

    cJSON_AddStringToObject(
        root,
        "version",
        VERSION
    );

    char *payload = cJSON_PrintUnformatted(root);

    if (payload == NULL) {
        cJSON_Delete(root);
        return;
    }

    int msg_id = mqtt_manager_publish(
        MQTT_TOPIC_INFO,
        payload,
        1,
        0
    );

    if (msg_id < 0) {
        ESP_LOGE(TAG, "Failed to publish info");
    }

    cJSON_free(payload);
    cJSON_Delete(root);
}


static void info_task(void *args)
{
    (void)args;

    while (1) {

        publish_info();

        vTaskDelay(
            pdMS_TO_TICKS(PUBLISH_PERIOD_MS)
        );
    }
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

    cJSON_AddStringToObject(
        root,
        "device_id",
        DEVICE_ID
    );

    cJSON_AddNumberToObject(
        root,
        "illuminance",
        lux
    );

    cJSON_AddStringToObject(
        root,
        "unit",
        "lux"
    );

    cJSON_AddNumberToObject(
        root,
        "serial",
        ++serial_number
    );

    cJSON_AddStringToObject(
        root,
        "timestamp",
        timestamp
    );

    char *payload = cJSON_PrintUnformatted(root);

    if (payload == NULL) {
        cJSON_Delete(root);
        return;
    }

    int msg_id = mqtt_manager_publish(
        MQTT_TOPIC_DATA,
        payload,
        1,
        0
    );

    if (msg_id < 0) {
        ESP_LOGE(TAG, "Failed to publish telemetry");
    }

    cJSON_free(payload);
    cJSON_Delete(root);
}


static void telemetry_task(void *arg)
{
    (void)arg;

    while (1) {

        publish_telemetry();

        vTaskDelay(
            pdMS_TO_TICKS(PUBLISH_PERIOD_MS)
        );
    }
}


// ---------- Application ----------

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

    // Network initialization.
    ESP_ERROR_CHECK(wifi_manager_init());
    wifi_manager_wait_connected();

    ESP_ERROR_CHECK(mqtt_manager_init());
    mqtt_manager_wait_connected();

    // Time synchronization requires network access.
    start_time_sync();
    wait_for_time_sync();

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

    if (xTaskCreate(
            info_task,
            "info",
            4096,
            NULL,
            5,
            NULL
        ) != pdPASS) {

        ESP_LOGE(TAG, "Failed to create info task");
    }
}