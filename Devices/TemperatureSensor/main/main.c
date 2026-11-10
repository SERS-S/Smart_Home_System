#include <time.h>
#include <stdio.h>
#include <stdint.h>

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#include "driver/gpio.h"

#include "esp_log.h"
#include "nvs_flash.h"
#include "cJSON.h"

static const char *TAG = "temperature sensor";

#include "wifi_manager.h"
#include "mqtt_manager.h"
#include "button.h"
#include "control.h"
#include "time_sync.h"
#include "publish_connection.h"
#include "info.h"

#include "bmp280.h"

#define DEVICE_ID "temperature_sensor_01"
#define VERSION   "0.1"

#define MQTT_TOPIC_DATA "telemetry/home/temperature/data"
#define MQTT_TOPIC_INFO "telemetry/home/temperature/info"
#define MQTT_TOPIC_SIGNAL "telemetry/home/temperature/signal"

#define PUBLISH_PERIOD_MS 120000
#define N_PROBS 30
#define SENSOR_PERIOD_MS (PUBLISH_PERIOD_MS) / (N_PROBS)

#define ESP_DISCONNECT 0x2001

#define LED_BUILTIN 2 

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

static float mean(float *array, int n)
{
    int i;
    float sum = 0;
    for (i = 0; i < n; i++) {
        sum += array[i];
    }

    return sum / n;
}

static esp_err_t n_times_read(float *result)
{
    float array[N_PROBS];
    float temperature;
    int i;

    for (i = 0; i < N_PROBS; i++) {
        esp_err_t err =
            bmp280_read_temperature(&temperature);
        if (err == ESP_OK) {

            array[i] = temperature;

            ESP_LOGI(
                TAG,
                "Temperature: %.2f C",
                temperature
            );

        } else {

            ESP_LOGE(
                TAG,
                "Temperature read failed: %s",
                esp_err_to_name(err)
            );

            return err;
        }
        vTaskDelay(
            pdMS_TO_TICKS(SENSOR_PERIOD_MS)
        );

        if (!bits_is_set()) {
            return ESP_DISCONNECT;
        }
    }

    *result = mean(array, N_PROBS);

    return ESP_OK;
}

static void telemetry_task(void *arg)
{
    (void)arg;

    while (1) {
        wait_bits();

        float temperature;

        esp_err_t err = n_times_read(&temperature);

        if (err == ESP_DISCONNECT) {
            continue;
        }

        if (err == ESP_OK) {

            ESP_LOGI(
                TAG,
                "Mean temperature: %.2f C",
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
    }
}


static void info_task(void *args)
{
    (void)args;

    while (1) {
        wait_bits();

        publish_info(
            DEVICE_ID,
            VERSION,
            MQTT_TOPIC_INFO
        );

        vTaskDelay(
            pdMS_TO_TICKS(PUBLISH_PERIOD_MS)
        );
    }
}

static void publish_connect(void)
{
    publish_connection(
        1,
        DEVICE_ID,
        MQTT_TOPIC_SIGNAL
    );
    gpio_set_level(LED_BUILTIN, 1);
}

static void publish_disconnect(void)
{
    publish_connection(
        0,
        DEVICE_ID,
        MQTT_TOPIC_SIGNAL
    );
    gpio_set_level(LED_BUILTIN, 0);
}

static void button_task(void *arg)
{
    (void)arg;

    while (1) {
        do_control(
            button_is_pressed,
            publish_disconnect,
            publish_connect
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

    voltage_init();

    gpio_reset_pin(LED_BUILTIN);
    gpio_set_direction(LED_BUILTIN, GPIO_MODE_OUTPUT);

    // Common network modules.
    ESP_ERROR_CHECK(wifi_manager_init());

    wifi_manager_wait_connected();

    ESP_ERROR_CHECK(mqtt_manager_init());

    mqtt_manager_wait_connected();

    time_sync();

    init_event_group(publish_connect);

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

    if (xTaskCreate(
            info_task,
            "button",
            4096,
            NULL,
            5,
            NULL
        ) != pdPASS) {

        ESP_LOGE(
            TAG,
            "Failed to create info task"
        );
    }
}