
idf_component_register(
    SRCS 
        "main.c"
        "bmp280.c"
    INCLUDE_DIRS 
        "."
    REQUIRES 
        esp_driver_i2c
        esp_driver_gpio
        nvs_flash
        components
)
