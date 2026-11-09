#include <time.h>

#include "esp_log.h"
#include "nvs_flash.h"
#include "cJSON.h"

#include "wifi_manager.h"
#include "mqtt_manager.h"

#define DEVICE_ID "temperature_sensor_01"
#define VERSION   "0.1"

#define MQTT_TOPIC_DATA "telemetry/home/temperature/data"
#define MQTT_TOPIC_INFO "telemetry/home/temperature/info"

#define PUBLISH_PERIOD_MS 60000

static int serial_number = 0;


static void publish_telemetry(float temperature)
{
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
        "temperature",
        temperature
    );

    cJSON_AddStringToObject(
        root,
        "unit",
        "celsius"
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
        ESP_LOGE(TAG, "Failed to serialize JSON");

        cJSON_Delete(root);
        return;
    }

    int msg_id = mqtt_manager_publish(
        MQTT_TOPIC_DATA,
        payload,
        1,
        0
    );

    if (msg_id >= 0) {

        ESP_LOGI(
            TAG,
            "Published to %s: %s",
            MQTT_TOPIC_DATA,
            payload
        );

    } else {

        ESP_LOGE(
            TAG,
            "MQTT publish failed"
        );
    }

    cJSON_free(payload);
    cJSON_Delete(root);
}


static void telemetry_task(void *arg)
{
    (void)arg;

    while (1) {

        float temperature;

        esp_err_t err =
            bmp280_read_temperature(&temperature);

        if (err == ESP_OK) {

            ESP_LOGI(
                TAG,
                "Temperature: %.2f C",
                temperature
            );

            publish_telemetry(temperature);

        } else {

            ESP_LOGE(
                TAG,
                "Temperature read failed: %s",
                esp_err_to_name(err)
            );
        }

        vTaskDelay(
            pdMS_TO_TICKS(PUBLISH_PERIOD_MS)
        );
    }
}

static void button_task(void *arg)
{
    (void)arg;

    while (1) {

        if (button_is_pressed()) {
            ESP_LOGI("BUTTON", "Button pressed");
        }

        vTaskDelay(
            pdMS_TO_TICKS(50)
        );
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

    ESP_LOGI(
        TAG,
        "Starting BMP280 temperature sensor"
    );

    ESP_ERROR_CHECK(bmp280_init());

    button_init();

    // Common network modules.
    ESP_ERROR_CHECK(wifi_manager_init());

    wifi_manager_wait_connected();

    ESP_ERROR_CHECK(mqtt_manager_init());

    mqtt_manager_wait_connected();

    if (xTaskCreate(
            telemetry_task,
            "telemetry",
            4096,
            NULL,
            5,
            NULL
        ) != pdPASS) {

        ESP_LOGE(
            TAG,
            "Failed to create telemetry task"
        );
    }

    if (xTaskCreate(
            button_task,
            "button",
            4096,
            NULL,
            5,
            NULL
        ) != pdPASS) {

        ESP_LOGE(
            TAG,
            "Failed to create button task"
        );
    }
}